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
echo -e "${YELLOW}[1/9] Creating kind cluster...${NC}"
if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster already exists, deleting..."
    kind delete cluster --name ${CLUSTER_NAME}
fi
kind create cluster --name ${CLUSTER_NAME}
echo -e "${GREEN}✓ Cluster created${NC}"
echo ""

# Step 2: Install Class CRD
echo -e "${YELLOW}[2/9] Installing Class CRD...${NC}"
kubectl apply -f config/crd/nerc.mghpcc.org_classes.yaml
sleep 2
kubectl get crd classes.nerc.mghpcc.org
echo -e "${GREEN}✓ CRD installed${NC}"
echo ""

# Step 3: Install OpenShift Group CRD (for multi-namespace testing)
echo -e "${YELLOW}[3/9] Installing OpenShift Group CRD...${NC}"
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
echo -e "${YELLOW}[4/9] Creating test OpenShift group...${NC}"
kubectl apply -f samples/test-group.yaml
kubectl get group cs202-students -o yaml | grep -A 5 "users:"
echo -e "${GREEN}✓ Test group created${NC}"
echo ""

# Step 5: Start operator in background
echo -e "${YELLOW}[5/9] Starting operator...${NC}"
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
echo -e "${YELLOW}[6/9] Testing single-namespace class creation...${NC}"
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
else
    echo -e "${RED}✗ Namespace not created${NC}"
    exit 1
fi
echo ""

# Step 7: Test multi-namespace class
echo -e "${YELLOW}[7/9] Testing multi-namespace class creation...${NC}"
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
else
    echo -e "${RED}✗ Expected $EXPECTED_COUNT namespaces, found $ACTUAL_COUNT${NC}"
    exit 1
fi
echo ""

# Step 8: Test dynamic user addition and reconciliation
echo -e "${YELLOW}[8/9] Testing dynamic user addition and reconciliation...${NC}"

# Add a new user to the group
echo "Adding user 'eve' to cs202-students group..."
kubectl patch group cs202-students --type=json -p='[{"op":"add","path":"/users/-","value":"eve"}]'
echo -e "${GREEN}✓ User added to group${NC}"

# Verify group was updated
GROUP_USERS=$(kubectl get group cs202-students -o jsonpath='{.users[*]}')
if [[ $GROUP_USERS == *"eve"* ]]; then
    echo -e "${GREEN}✓ Group updated successfully${NC}"
else
    echo -e "${RED}✗ User not found in group${NC}"
    exit 1
fi

# Trigger reconciliation by annotating the class
echo "Triggering reconciliation..."
kubectl annotate class multi-test-class reconcile-trigger=$(date +%s) --overwrite
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
else
    echo -e "${RED}✗ Namespace for eve was not created after reconciliation${NC}"
    echo "Current namespaces:"
    kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class
    exit 1
fi
echo ""

# Step 9: Test namespace cleanup on deletion
echo -e "${YELLOW}[9/9] Testing namespace cleanup on class deletion...${NC}"

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
echo "  ✓ Multi-namespace class created (4 namespaces with hash suffix)"
echo "  ✓ Dynamic user addition triggers reconciliation (5th namespace created)"
echo "  ✓ Namespace cleanup works on class deletion (finalizers)"
echo ""
echo "Operator logs saved to: /tmp/operator-test.log"
echo ""
