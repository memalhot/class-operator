#!/bin/bash
set -e

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test configuration
CLUSTER_NAME="class-operator-test"
TEST_TIMEOUT=30

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Class Operator Test Suite${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    # Kill operator process
    if [ ! -z "$OPERATOR_PID" ] && ps -p $OPERATOR_PID > /dev/null 2>&1; then
        kill $OPERATOR_PID 2>/dev/null || true
        sleep 1
    fi

    # Kill any remaining operator processes
    pkill -f "go run main.go" 2>/dev/null || true

    # Free up port 8080
    lsof -ti:8080 2>/dev/null | xargs kill -9 2>/dev/null || true

    # Delete cluster
    kind delete cluster --name ${CLUSTER_NAME} 2>/dev/null || true
}

# Set trap to cleanup on exit
trap cleanup EXIT

# Initial cleanup of any existing operator processes
echo -e "${YELLOW}Cleaning up any existing operator processes...${NC}"
pkill -f "go run main.go" 2>/dev/null || true
lsof -ti:8080 2>/dev/null | xargs kill -9 2>/dev/null || true
sleep 2
echo ""

# Change to project root directory
cd "$(dirname "$0")/.."

# Step 1: Create kind cluster
echo -e "${YELLOW}[1/11] Creating kind cluster...${NC}"
if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster already exists, deleting..."
    kind delete cluster --name ${CLUSTER_NAME}
fi
kind create cluster --name ${CLUSTER_NAME}
echo -e "${GREEN}✓ Cluster created${NC}"
echo ""

# Step 2: Install Class CRD
echo -e "${YELLOW}[2/11] Installing Class CRD...${NC}"
kubectl apply -f config/crd/nerc.mghpcc.org_classes.yaml
sleep 2
kubectl get crd classes.nerc.mghpcc.org
echo -e "${GREEN}✓ CRD installed${NC}"
echo ""

# Step 3: Install OpenShift Group CRD (for multi-namespace testing)
echo -e "${YELLOW}[3/11] Installing OpenShift Group CRD...${NC}"
cat <<EOF | kubectl apply -f -
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: groups.user.openshift.io
spec:
  group: user.openshift.io
  names:
    kind: Group
    listKind: GroupList
    plural: groups
    singular: group
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          apiVersion:
            type: string
          kind:
            type: string
          metadata:
            type: object
          users:
            type: array
            items:
              type: string
EOF
sleep 2
kubectl get crd groups.user.openshift.io
echo -e "${GREEN}✓ Group CRD installed${NC}"
echo ""

# Step 4: Create test group
echo -e "${YELLOW}[4/11] Creating test OpenShift group...${NC}"
kubectl apply -f samples/test-group.yaml
kubectl get group students -o yaml | grep -A 5 "users:"
echo -e "${GREEN}✓ Test group created${NC}"
echo ""

# Step 5: Start operator in background
echo -e "${YELLOW}[5/11] Starting operator...${NC}"
go run main.go > /tmp/operator-test.log 2>&1 &
OPERATOR_PID=$!
sleep 5

# Check if operator is running
if ! ps -p $OPERATOR_PID > /dev/null; then
    echo -e "${RED}✗ Operator failed to start${NC}"
    echo "Log output:"
    cat /tmp/operator-test.log
    exit 1
fi
echo -e "${GREEN}✓ Operator started (PID: $OPERATOR_PID)${NC}"
echo ""

# Step 6: Test single-namespace class
echo -e "${YELLOW}[6/11] Testing single-namespace class creation...${NC}"
kubectl apply -f samples/single-namespace-class.yaml
sleep 3

# Verify single-namespace
NAMESPACE_NAME=$(kubectl get class test-class -o jsonpath='{.status.namespaces[0]}' 2>/dev/null || echo "")
if [ -z "$NAMESPACE_NAME" ]; then
    echo -e "${RED}✗ Failed to create single-namespace class${NC}"
    exit 1
fi

kubectl get namespace $NAMESPACE_NAME > /dev/null 2>&1
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Single-namespace created: $NAMESPACE_NAME${NC}"

    # Verify labels
    CLASS_LABEL=$(kubectl get namespace $NAMESPACE_NAME -o jsonpath='{.metadata.labels.nerc\.mghpcc\.org/class}')
    if [ "$CLASS_LABEL" == "test-class" ]; then
        echo -e "${GREEN}✓ Namespace labels verified${NC}"
    else
        echo -e "${RED}✗ Namespace labels incorrect${NC}"
        exit 1
    fi

    # Verify finalizer
    FINALIZER=$(kubectl get class test-class -o jsonpath='{.metadata.finalizers[0]}')
    if [ "$FINALIZER" == "nerc.mghpcc.org/class-finalizer" ]; then
        echo -e "${GREEN}✓ Finalizer added to class${NC}"
    else
        echo -e "${RED}✗ Finalizer not found${NC}"
        exit 1
    fi

    # Verify RoleBindings for all users in single-namespace
    echo "Verifying RoleBindings for users in shared namespace..."
    EXPECTED_USERS=("alice" "bob" "charlie" "david")
    for user in "${EXPECTED_USERS[@]}"; do
        ROLEBINDING_NAME="${user}-edit"
        kubectl get rolebinding $ROLEBINDING_NAME -n $NAMESPACE_NAME > /dev/null 2>&1
        if [ $? -eq 0 ]; then
            # Verify RoleBinding grants edit permissions
            ROLE_REF=$(kubectl get rolebinding $ROLEBINDING_NAME -n $NAMESPACE_NAME -o jsonpath='{.roleRef.name}')
            if [ "$ROLE_REF" == "edit" ]; then
                echo -e "${GREEN}✓ RoleBinding $ROLEBINDING_NAME grants edit permissions${NC}"
            else
                echo -e "${RED}✗ RoleBinding $ROLEBINDING_NAME does not grant edit permissions${NC}"
                exit 1
            fi
        else
            echo -e "${RED}✗ RoleBinding $ROLEBINDING_NAME not found in namespace $NAMESPACE_NAME${NC}"
            exit 1
        fi
    done
    echo -e "${GREEN}✓ All users have edit permissions in shared namespace${NC}"
else
    echo -e "${RED}✗ Namespace not created${NC}"
    exit 1
fi
echo ""

# Step 7: Test multi-namespace class
echo -e "${YELLOW}[7/11] Testing multi-namespace class creation...${NC}"
kubectl apply -f samples/multi-namespace-class.yaml
sleep 5

# Verify multi-namespaces
EXPECTED_COUNT=4
sleep 5
ACTUAL_COUNT=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class --no-headers | wc -l)

if [ "$ACTUAL_COUNT" -eq "$EXPECTED_COUNT" ]; then
    echo -e "${GREEN}✓ Multi-namespaces created: $ACTUAL_COUNT namespaces${NC}"
    kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class

    # Verify hash suffix
    NAMESPACE_WITH_HASH=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[0].metadata.name}')
    if [[ $NAMESPACE_WITH_HASH =~ -[a-f0-9]{6}$ ]]; then
        echo -e "${GREEN}✓ Namespaces have hash suffix${NC}"
    else
        echo -e "${RED}✗ Namespaces missing hash suffix${NC}"
        exit 1
    fi

    # Verify RoleBindings for each user in their own namespace
    echo "Verifying RoleBindings in multi-namespace mode..."
    MULTI_USERS=("alice" "bob" "charlie" "david")
    for user in "${MULTI_USERS[@]}"; do
        # Find the namespace for this user
        USER_NAMESPACE=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "cs202-${user}-")
        if [ ! -z "$USER_NAMESPACE" ]; then
            ROLEBINDING_NAME="${user}-edit"
            kubectl get rolebinding $ROLEBINDING_NAME -n $USER_NAMESPACE > /dev/null 2>&1
            if [ $? -eq 0 ]; then
                # Verify RoleBinding grants edit permissions
                ROLE_REF=$(kubectl get rolebinding $ROLEBINDING_NAME -n $USER_NAMESPACE -o jsonpath='{.roleRef.name}')
                SUBJECT=$(kubectl get rolebinding $ROLEBINDING_NAME -n $USER_NAMESPACE -o jsonpath='{.subjects[0].name}')
                if [ "$ROLE_REF" == "edit" ] && [ "$SUBJECT" == "$user" ]; then
                    echo -e "${GREEN}✓ User $user has edit permissions in namespace $USER_NAMESPACE${NC}"
                else
                    echo -e "${RED}✗ RoleBinding for $user is incorrect (role: $ROLE_REF, subject: $SUBJECT)${NC}"
                    exit 1
                fi
            else
                echo -e "${RED}✗ RoleBinding $ROLEBINDING_NAME not found in namespace $USER_NAMESPACE${NC}"
                exit 1
            fi
        else
            echo -e "${RED}✗ Namespace for user $user not found${NC}"
            exit 1
        fi
    done
    echo -e "${GREEN}✓ All users have edit permissions in their respective namespaces${NC}"
else
    echo -e "${RED}✗ Expected $EXPECTED_COUNT namespaces, found $ACTUAL_COUNT${NC}"
    exit 1
fi
echo ""

# Step 8: Test dynamic user addition and reconciliation
echo -e "${YELLOW}[8/11] Testing dynamic user addition and reconciliation...${NC}"

# Add a new user to the group
echo "Adding user 'eve' to students group..."
kubectl patch group students --type=json -p='[{"op":"add","path":"/users/-","value":"eve"}]'
echo -e "${GREEN}✓ User added to group${NC}"

# Verify group was updated
GROUP_USERS=$(kubectl get group students -o jsonpath='{.users[*]}')
if [[ $GROUP_USERS == *"eve"* ]]; then
    echo -e "${GREEN}✓ Group updated successfully${NC}"
else
    echo -e "${RED}✗ User not found in group${NC}"
    exit 1
fi

# Reconciliation should happen automatically via group watch
echo "Waiting for automatic reconciliation (group watch)..."
sleep 5

# Wait for operator to reconcile (may take a moment)
echo "Waiting for namespace creation (up to 15 seconds)..."
for i in {1..15}; do
    EVE_NAMESPACE=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[*].metadata.name}' | grep -o 'cs202-eve-[a-f0-9]\{6\}' || echo "")
    if [ ! -z "$EVE_NAMESPACE" ]; then
        break
    fi
    sleep 1
    echo -n "."
done
echo ""

if [ ! -z "$EVE_NAMESPACE" ]; then
    echo -e "${GREEN}✓ New namespace created for eve: $EVE_NAMESPACE${NC}"

    # Verify it has the correct labels
    CLASS_LABEL=$(kubectl get namespace $EVE_NAMESPACE -o jsonpath='{.metadata.labels.nerc\.mghpcc\.org/class}')
    if [ "$CLASS_LABEL" == "multi-test-class" ]; then
        echo -e "${GREEN}✓ New namespace has correct labels${NC}"
    else
        echo -e "${RED}✗ New namespace labels incorrect${NC}"
        exit 1
    fi

    # Verify total namespace count is now 5
    TOTAL_COUNT=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class --no-headers | wc -l)
    if [ "$TOTAL_COUNT" -eq 5 ]; then
        echo -e "${GREEN}✓ Total namespaces: $TOTAL_COUNT (alice, bob, charlie, david, eve)${NC}"
    else
        echo -e "${RED}✗ Expected 5 namespaces, found $TOTAL_COUNT${NC}"
        kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class
        exit 1
    fi

    # Verify RoleBinding was created for the newly added user
    echo "Verifying RoleBinding for newly added user eve..."
    ROLEBINDING_NAME="eve-edit"
    kubectl get rolebinding $ROLEBINDING_NAME -n $EVE_NAMESPACE > /dev/null 2>&1
    if [ $? -eq 0 ]; then
        # Verify RoleBinding grants edit permissions
        ROLE_REF=$(kubectl get rolebinding $ROLEBINDING_NAME -n $EVE_NAMESPACE -o jsonpath='{.roleRef.name}')
        SUBJECT=$(kubectl get rolebinding $ROLEBINDING_NAME -n $EVE_NAMESPACE -o jsonpath='{.subjects[0].name}')
        if [ "$ROLE_REF" == "edit" ] && [ "$SUBJECT" == "eve" ]; then
            echo -e "${GREEN}✓ User eve has edit permissions in namespace $EVE_NAMESPACE${NC}"
        else
            echo -e "${RED}✗ RoleBinding for eve is incorrect (role: $ROLE_REF, subject: $SUBJECT)${NC}"
            exit 1
        fi
    else
        echo -e "${RED}✗ RoleBinding $ROLEBINDING_NAME not found in namespace $EVE_NAMESPACE${NC}"
        exit 1
    fi
else
    echo -e "${RED}✗ Namespace for eve was not created after reconciliation${NC}"
    echo "Current namespaces:"
    kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class
    exit 1
fi
echo ""

# Step 9: Test user removal and cleanup
echo -e "${YELLOW}[9/11] Testing user removal and RoleBinding/namespace cleanup...${NC}"

# Test 9a: Remove user from group in multi-namespace mode
echo "Removing user 'alice' from students group..."
kubectl patch group students --type=json -p='[{"op":"remove","path":"/users/0"}]'
GROUP_USERS_AFTER=$(kubectl get group students -o jsonpath='{.users[*]}')
if [[ $GROUP_USERS_AFTER != *"alice"* ]]; then
    echo -e "${GREEN}✓ User alice removed from group${NC}"
else
    echo -e "${RED}✗ User alice still in group${NC}"
    exit 1
fi

# Wait for reconciliation
echo "Waiting for automatic reconciliation after user removal..."
sleep 5

# Verify alice's namespace was deleted
ALICE_NAMESPACE=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[*].metadata.name}' | grep -o 'cs202-alice-[a-f0-9]\{6\}' || echo "")
if [ -z "$ALICE_NAMESPACE" ]; then
    echo -e "${GREEN}✓ Namespace for removed user alice was deleted${NC}"
else
    echo -e "${RED}✗ Namespace for alice still exists: $ALICE_NAMESPACE${NC}"
    exit 1
fi

# Verify total namespace count is now 4 (bob, charlie, david, eve)
TOTAL_COUNT=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class --no-headers | wc -l)
if [ "$TOTAL_COUNT" -eq 4 ]; then
    echo -e "${GREEN}✓ Correct number of namespaces after user removal: $TOTAL_COUNT${NC}"
else
    echo -e "${RED}✗ Expected 4 namespaces, found $TOTAL_COUNT${NC}"
    kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class
    exit 1
fi

# Test 9b: Remove user from group in single-namespace mode
echo "Removing user 'bob' from students group..."
kubectl patch group students --type=json -p='[{"op":"remove","path":"/users/0"}]'
GROUP_USERS_FINAL=$(kubectl get group students -o jsonpath='{.users[*]}')
if [[ $GROUP_USERS_FINAL != *"bob"* ]]; then
    echo -e "${GREEN}✓ User bob removed from group${NC}"
else
    echo -e "${RED}✗ User bob still in group${NC}"
    exit 1
fi

# Wait for reconciliation
echo "Waiting for automatic reconciliation in single-namespace mode..."
sleep 5

# Verify bob's RoleBinding was deleted from single-namespace
kubectl get rolebinding bob-edit -n $NAMESPACE_NAME > /dev/null 2>&1
if [ $? -ne 0 ]; then
    echo -e "${GREEN}✓ RoleBinding for removed user bob was deleted from shared namespace${NC}"
else
    echo -e "${RED}✗ RoleBinding for bob still exists in shared namespace${NC}"
    exit 1
fi

# Verify other users' RoleBindings still exist
REMAINING_USERS=("charlie" "david" "eve")
for user in "${REMAINING_USERS[@]}"; do
    kubectl get rolebinding ${user}-edit -n $NAMESPACE_NAME > /dev/null 2>&1
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✓ RoleBinding for $user still exists in shared namespace${NC}"
    else
        echo -e "${RED}✗ RoleBinding for $user was incorrectly deleted${NC}"
        exit 1
    fi
done

echo -e "${GREEN}✓ User removal cleanup works correctly${NC}"
echo ""

# Step 10: Test namespace cleanup on deletion
echo -e "${YELLOW}[10/11] Testing namespace cleanup on class deletion...${NC}"

# Delete multi-namespace class
echo "Deleting multi-namespace class..."
kubectl delete class multi-test-class
sleep 5

# Verify namespaces were deleted
REMAINING=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class --no-headers 2>/dev/null | wc -l)
if [ "$REMAINING" -eq 0 ]; then
    echo -e "${GREEN}✓ Multi-namespace class namespaces cleaned up${NC}"
else
    echo -e "${RED}✗ $REMAINING namespaces still exist after class deletion${NC}"
    kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class
    exit 1
fi

# Delete single-namespace class
echo "Deleting single-namespace class..."
kubectl delete class test-class
sleep 5

# Verify namespace was deleted
kubectl get namespace $NAMESPACE_NAME > /dev/null 2>&1
if [ $? -ne 0 ]; then
    echo -e "${GREEN}✓ Single-namespace class namespace cleaned up${NC}"
else
    echo -e "${RED}✗ Namespace $NAMESPACE_NAME still exists after class deletion${NC}"
    exit 1
fi
echo ""

# Final summary
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}All tests passed! ✓${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Test Summary:"
echo "  ✓ Kind cluster created and configured"
echo "  ✓ CRDs installed (Class, Group)"
echo "  ✓ Operator started successfully"
echo "  ✓ Single-namespace class created with labels and finalizer"
echo "  ✓ RoleBindings created for all users in single-namespace mode (edit permissions)"
echo "  ✓ Multi-namespace class created (4 namespaces with hash suffix)"
echo "  ✓ RoleBindings created for each user in their own namespace (edit permissions)"
echo "  ✓ Dynamic user addition triggers reconciliation (5th namespace created)"
echo "  ✓ RoleBinding created for newly added user with edit permissions"
echo "  ✓ User removal from group triggers cleanup:"
echo "    - Namespace deleted in multi-namespace mode"
echo "    - RoleBinding deleted in single-namespace mode"
echo "  ✓ Namespace cleanup works on class deletion (finalizers)"
echo ""
echo "Operator logs saved to: /tmp/operator-test.log"
echo ""
