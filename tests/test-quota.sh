#!/bin/bash
set -e

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test configuration
CLUSTER_NAME="class-operator-quota-test"
TEST_TIMEOUT=30

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}ResourceQuota Test Suite${NC}"
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
echo -e "${YELLOW}[1/10] Creating kind cluster...${NC}"
if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster already exists, deleting..."
    kind delete cluster --name ${CLUSTER_NAME}
fi
kind create cluster --name ${CLUSTER_NAME}
echo -e "${GREEN}✓ Cluster created${NC}"
echo ""

# Step 2: Install Class CRD
echo -e "${YELLOW}[2/10] Installing Class CRD...${NC}"
kubectl apply -f config/crd/nerc.mghpcc.org_classes.yaml
sleep 2
kubectl get crd classes.nerc.mghpcc.org
echo -e "${GREEN}✓ CRD installed${NC}"
echo ""

# Step 3: Install OpenShift Group CRD
echo -e "${YELLOW}[3/10] Installing OpenShift Group CRD...${NC}"
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
echo -e "${YELLOW}[4/10] Creating test OpenShift group...${NC}"
kubectl apply -f tests/test-samples/test-group.yaml
kubectl get group students -o yaml | grep -A 5 "users:"
echo -e "${GREEN}✓ Test group created${NC}"
echo ""

# Step 5: Start operator in background
echo -e "${YELLOW}[5/10] Starting operator...${NC}"
go run main.go > /tmp/operator-quota-test.log 2>&1 &
OPERATOR_PID=$!
sleep 5

# Check if operator is running
if ! ps -p $OPERATOR_PID > /dev/null; then
    echo -e "${RED}✗ Operator failed to start${NC}"
    echo "Log output:"
    cat /tmp/operator-quota-test.log
    exit 1
fi
echo -e "${GREEN}✓ Operator started (PID: $OPERATOR_PID)${NC}"
echo ""

# Step 6: Test multi-namespace class creates ResourceQuotas
echo -e "${YELLOW}[6/10] Testing ResourceQuota creation in multi-namespace mode...${NC}"
kubectl apply -f tests/test-samples/multi-namespace-class.yaml
sleep 5

# Verify multi-namespaces were created
EXPECTED_COUNT=4
ACTUAL_COUNT=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class --no-headers | wc -l)

if [ "$ACTUAL_COUNT" -eq "$EXPECTED_COUNT" ]; then
    echo -e "${GREEN}✓ Multi-namespaces created: $ACTUAL_COUNT namespaces${NC}"

    # Check each namespace for ResourceQuota
    MULTI_USERS=("alice" "bob" "charlie" "david")
    for user in "${MULTI_USERS[@]}"; do
        # Find the namespace for this user
        USER_NAMESPACE=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "cs202-${user}-")

        if [ ! -z "$USER_NAMESPACE" ]; then
            # Check if ResourceQuota exists
            set +e
            kubectl get resourcequota class-quota -n $USER_NAMESPACE > /dev/null 2>&1
            QUOTA_CHECK=$?
            set -e
            if [ $QUOTA_CHECK -eq 0 ]; then
                echo -e "${GREEN}✓ ResourceQuota exists in namespace $USER_NAMESPACE${NC}"

                # Verify quota values
                CPU_LIMIT=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.spec.hard.limits\.cpu}')
                MEMORY_LIMIT=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.spec.hard.limits\.memory}')
                PODS_LIMIT=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.spec.hard.pods}')

                # Expected values from multi-namespace-class.yaml
                if [ "$CPU_LIMIT" == "2" ] && [ "$MEMORY_LIMIT" == "4Gi" ] && [ "$PODS_LIMIT" == "5" ]; then
                    echo -e "${GREEN}✓ ResourceQuota values correct for $user (CPU: 2, Memory: 4Gi, Pods: 5)${NC}"
                else
                    echo -e "${RED}✗ ResourceQuota values incorrect for $user${NC}"
                    echo "  Expected: CPU=2, Memory=4Gi, Pods=5"
                    echo "  Got: CPU=$CPU_LIMIT, Memory=$MEMORY_LIMIT, Pods=$PODS_LIMIT"
                    exit 1
                fi

                # Verify labels
                QUOTA_LABEL=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.metadata.labels.nerc\.mghpcc\.org/managed-by}')
                if [ "$QUOTA_LABEL" == "class-operator" ]; then
                    echo -e "${GREEN}✓ ResourceQuota has correct labels${NC}"
                else
                    echo -e "${RED}✗ ResourceQuota labels incorrect${NC}"
                    exit 1
                fi
            else
                echo -e "${RED}✗ ResourceQuota not found in namespace $USER_NAMESPACE${NC}"
                exit 1
            fi
        else
            echo -e "${RED}✗ Namespace for user $user not found${NC}"
            exit 1
        fi
    done
else
    echo -e "${RED}✗ Expected $EXPECTED_COUNT namespaces, found $ACTUAL_COUNT${NC}"
    exit 1
fi
echo ""

# Step 7: Test single-namespace class does NOT create ResourceQuota
echo -e "${YELLOW}[7/10] Testing that single-namespace mode does NOT create ResourceQuota...${NC}"
kubectl apply -f tests/test-samples/single-namespace-class.yaml
sleep 3

# Get the namespace name
SINGLE_NAMESPACE=$(kubectl get class test-class -o jsonpath='{.status.namespaces[0]}' 2>/dev/null || echo "")
if [ -z "$SINGLE_NAMESPACE" ]; then
    echo -e "${RED}✗ Failed to create single-namespace class${NC}"
    exit 1
fi

# Verify namespace exists
kubectl get namespace $SINGLE_NAMESPACE > /dev/null 2>&1
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Single-namespace created: $SINGLE_NAMESPACE${NC}"

    # Verify ResourceQuota does NOT exist (single-namespace should not get quotas)
    set +e
    kubectl get resourcequota class-quota -n $SINGLE_NAMESPACE > /dev/null 2>&1
    QUOTA_EXISTS=$?
    set -e
    if [ $QUOTA_EXISTS -ne 0 ]; then
        echo -e "${GREEN}✓ ResourceQuota correctly NOT created in single-namespace mode${NC}"
    else
        echo -e "${RED}✗ ResourceQuota should not exist in single-namespace mode${NC}"
        exit 1
    fi
else
    echo -e "${RED}✗ Single namespace not created${NC}"
    exit 1
fi
echo ""

# Step 8: Test dynamic user addition creates ResourceQuota
echo -e "${YELLOW}[8/10] Testing ResourceQuota creation for dynamically added user...${NC}"

# Add a new user to the group
echo "Adding user 'eve' to students group..."
kubectl patch group students --type=json -p='[{"op":"add","path":"/users/-","value":"eve"}]'
echo -e "${GREEN}✓ User added to group${NC}"

# Wait for reconciliation
echo "Waiting for automatic reconciliation..."
sleep 5

# Wait for namespace creation
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

    # Verify ResourceQuota was created
    set +e
    kubectl get resourcequota class-quota -n $EVE_NAMESPACE > /dev/null 2>&1
    EVE_QUOTA_CHECK=$?
    set -e
    if [ $EVE_QUOTA_CHECK -eq 0 ]; then
        echo -e "${GREEN}✓ ResourceQuota created for new user eve${NC}"

        # Verify quota values
        CPU_LIMIT=$(kubectl get resourcequota class-quota -n $EVE_NAMESPACE -o jsonpath='{.spec.hard.limits\.cpu}')
        MEMORY_LIMIT=$(kubectl get resourcequota class-quota -n $EVE_NAMESPACE -o jsonpath='{.spec.hard.limits\.memory}')
        PODS_LIMIT=$(kubectl get resourcequota class-quota -n $EVE_NAMESPACE -o jsonpath='{.spec.hard.pods}')

        if [ "$CPU_LIMIT" == "2" ] && [ "$MEMORY_LIMIT" == "4Gi" ] && [ "$PODS_LIMIT" == "5" ]; then
            echo -e "${GREEN}✓ ResourceQuota values correct for dynamically added user eve${NC}"
        else
            echo -e "${RED}✗ ResourceQuota values incorrect for eve${NC}"
            exit 1
        fi
    else
        echo -e "${RED}✗ ResourceQuota not found for new user eve${NC}"
        exit 1
    fi
else
    echo -e "${RED}✗ Namespace for eve was not created${NC}"
    exit 1
fi
echo ""

# Step 9: Test ResourceQuota enforcement - try to exceed limits
echo -e "${YELLOW}[9/12] Testing ResourceQuota enforcement by exceeding limits...${NC}"

# Use alice's namespace for testing
ALICE_NAMESPACE=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "cs202-bob-" | head -1)

if [ -z "$ALICE_NAMESPACE" ]; then
    echo -e "${RED}✗ Could not find test namespace${NC}"
    exit 1
fi

echo "Testing in namespace: $ALICE_NAMESPACE"
echo "Current quota: CPU=2, Memory=4Gi, Pods=5"

# Test 9a: Try to create a pod that exceeds CPU quota
echo "Test 9a: Attempting to create pod exceeding CPU quota (requesting 3 CPUs, quota is 2)..."
cat <<EOF | kubectl apply -f - 2>&1 | tee /tmp/quota-test-cpu.log || true
apiVersion: v1
kind: Pod
metadata:
  name: cpu-exceed-test
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        cpu: "3"
      limits:
        cpu: "3"
EOF

# Check if the pod creation was blocked
if grep -q "exceeded quota" /tmp/quota-test-cpu.log || grep -q "forbidden" /tmp/quota-test-cpu.log; then
    echo -e "${GREEN}✓ Pod creation correctly blocked due to CPU quota${NC}"
elif kubectl get pod cpu-exceed-test -n $ALICE_NAMESPACE > /dev/null 2>&1; then
    echo -e "${RED}✗ Pod was created despite exceeding CPU quota${NC}"
    kubectl delete pod cpu-exceed-test -n $ALICE_NAMESPACE --force --grace-period=0 2>/dev/null || true
    exit 1
else
    echo -e "${GREEN}✓ Pod creation blocked (quota enforcement working)${NC}"
fi

# Test 9b: Try to create a pod that exceeds Memory quota
echo "Test 9b: Attempting to create pod exceeding Memory quota (requesting 6Gi, quota is 4Gi)..."
cat <<EOF | kubectl apply -f - 2>&1 | tee /tmp/quota-test-memory.log || true
apiVersion: v1
kind: Pod
metadata:
  name: memory-exceed-test
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        memory: "6Gi"
      limits:
        memory: "6Gi"
EOF

# Check if the pod creation was blocked
if grep -q "exceeded quota" /tmp/quota-test-memory.log || grep -q "forbidden" /tmp/quota-test-memory.log; then
    echo -e "${GREEN}✓ Pod creation correctly blocked due to Memory quota${NC}"
elif kubectl get pod memory-exceed-test -n $ALICE_NAMESPACE > /dev/null 2>&1; then
    echo -e "${RED}✗ Pod was created despite exceeding Memory quota${NC}"
    kubectl delete pod memory-exceed-test -n $ALICE_NAMESPACE --force --grace-period=0 2>/dev/null || true
    exit 1
else
    echo -e "${GREEN}✓ Pod creation blocked (quota enforcement working)${NC}"
fi

# Test 9c: Create pods within quota to verify it works
echo "Test 9c: Creating pods within quota limits to verify quota allows valid pods..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: valid-pod-1
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        cpu: "500m"
        memory: "1Gi"
      limits:
        cpu: "500m"
        memory: "1Gi"
EOF

sleep 2

# Verify the pod was created
set +e
kubectl get pod valid-pod-1 -n $ALICE_NAMESPACE > /dev/null 2>&1
POD_CHECK=$?
set -e
if [ $POD_CHECK -eq 0 ]; then
    echo -e "${GREEN}✓ Pod within quota limits created successfully${NC}"
else
    echo -e "${RED}✗ Pod within quota limits failed to create${NC}"
    exit 1
fi

# Test 9d: Test pod count quota by creating 5 pods (the limit)
echo "Test 9d: Testing pod count quota (limit is 5 pods)..."
for i in {2..5}; do
    cat <<EOF | kubectl apply -f - > /dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: valid-pod-$i
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        cpu: "100m"
        memory: "256Mi"
      limits:
        cpu: "100m"
        memory: "256Mi"
EOF
done

sleep 2

# Count pods
POD_COUNT=$(kubectl get pods -n $ALICE_NAMESPACE --no-headers 2>/dev/null | wc -l)
echo "Created $POD_COUNT pods (quota limit is 5)"

# Try to create a 6th pod (should fail)
echo "Attempting to create 6th pod (should exceed pod count quota)..."
cat <<EOF | kubectl apply -f - 2>&1 | tee /tmp/quota-test-pods.log || true
apiVersion: v1
kind: Pod
metadata:
  name: valid-pod-6
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        cpu: "100m"
        memory: "256Mi"
      limits:
        cpu: "100m"
        memory: "256Mi"
EOF

# Check if the 6th pod creation was blocked
if grep -q "exceeded quota" /tmp/quota-test-pods.log || grep -q "forbidden" /tmp/quota-test-pods.log; then
    echo -e "${GREEN}✓ 6th pod creation correctly blocked due to pod count quota${NC}"
elif kubectl get pod valid-pod-6 -n $ALICE_NAMESPACE > /dev/null 2>&1; then
    echo -e "${RED}✗ 6th pod was created despite exceeding pod count quota${NC}"
    exit 1
else
    echo -e "${GREEN}✓ Pod count quota enforcement working${NC}"
fi

# Check the ResourceQuota status
echo "Checking ResourceQuota usage..."
kubectl get resourcequota class-quota -n $ALICE_NAMESPACE -o yaml | grep -A 10 "status:"

# Clean up test pods
echo "Cleaning up test pods..."
kubectl delete pods --all -n $ALICE_NAMESPACE --force --grace-period=0 2>/dev/null || true
sleep 2
echo -e "${GREEN}✓ ResourceQuota enforcement tests passed${NC}"
echo ""

# Step 10: Test ResourceQuota update when class spec changes
echo -e "${YELLOW}[10/12] Testing ResourceQuota update when class spec changes...${NC}"

# Update the class with new quota values
echo "Updating multi-test-class with new quota values..."
cat <<EOF | kubectl apply -f -
apiVersion: nerc.mghpcc.org/v1alpha1
kind: Class
metadata:
  name: multi-test-class
  namespace: default
spec:
  classCode: "CS202"
  semester: "Spring2026"
  displayName: "Multi-Namespace Test Class"
  professors:
    - "professor@university.edu"
  deployment:
    multiNamespace: true
    namespaceTemplate: "cs202-{{.Username}}"
    studentNamespacePrefix: "cs202-"
  studentsGroup: "students"
  resourceQuota:
    cpu: "4"
    memory: "8Gi"
    pods: "10"
  notebookCulling:
    enabled: true
    cutoff: 3600
EOF
echo -e "${GREEN}✓ Class spec updated${NC}"

# Wait for reconciliation
echo "Waiting for quota update reconciliation..."
sleep 5

# Check if quotas were updated
echo "Verifying quota updates..."
for user in "alice" "bob" "charlie" "david" "eve"; do
    USER_NAMESPACE=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "cs202-${user}-" || echo "")

    if [ ! -z "$USER_NAMESPACE" ]; then
        CPU_LIMIT=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.spec.hard.limits\.cpu}')
        MEMORY_LIMIT=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.spec.hard.limits\.memory}')
        PODS_LIMIT=$(kubectl get resourcequota class-quota -n $USER_NAMESPACE -o jsonpath='{.spec.hard.pods}')

        if [ "$CPU_LIMIT" == "4" ] && [ "$MEMORY_LIMIT" == "8Gi" ] && [ "$PODS_LIMIT" == "10" ]; then
            echo -e "${GREEN}✓ ResourceQuota updated for $user (CPU: 4, Memory: 8Gi, Pods: 10)${NC}"
        else
            echo -e "${RED}✗ ResourceQuota not updated for $user${NC}"
            echo "  Expected: CPU=4, Memory=8Gi, Pods=10"
            echo "  Got: CPU=$CPU_LIMIT, Memory=$MEMORY_LIMIT, Pods=$PODS_LIMIT"
            exit 1
        fi
    fi
done
echo ""

# Step 11: Test enforcement of updated quotas
echo -e "${YELLOW}[11/12] Testing enforcement of updated quota limits...${NC}"

# Use the same namespace
echo "Testing updated quota enforcement in namespace: $ALICE_NAMESPACE"
echo "Updated quota: CPU=4, Memory=8Gi, Pods=10"

# Try to create a pod with 3 CPUs (should succeed now with 4 CPU quota)
echo "Attempting to create pod with 3 CPUs (should succeed with updated 4 CPU quota)..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: cpu-within-new-quota
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        cpu: "3"
      limits:
        cpu: "3"
EOF

sleep 2

set +e
kubectl get pod cpu-within-new-quota -n $ALICE_NAMESPACE > /dev/null 2>&1
POD_3CPU_CHECK=$?
set -e
if [ $POD_3CPU_CHECK -eq 0 ]; then
    echo -e "${GREEN}✓ Pod with 3 CPUs created successfully with updated quota${NC}"
    kubectl delete pod cpu-within-new-quota -n $ALICE_NAMESPACE --force --grace-period=0 2>/dev/null || true
else
    echo -e "${RED}✗ Pod with 3 CPUs should be allowed with updated 4 CPU quota${NC}"
    exit 1
fi

# Try to create a pod with 5 CPUs (should fail even with updated quota)
echo "Attempting to create pod with 5 CPUs (should still exceed updated 4 CPU quota)..."
cat <<EOF | kubectl apply -f - 2>&1 | tee /tmp/quota-test-updated-cpu.log || true
apiVersion: v1
kind: Pod
metadata:
  name: cpu-exceed-updated-quota
  namespace: $ALICE_NAMESPACE
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    resources:
      requests:
        cpu: "5"
      limits:
        cpu: "5"
EOF

if grep -q "exceeded quota" /tmp/quota-test-updated-cpu.log || grep -q "forbidden" /tmp/quota-test-updated-cpu.log; then
    echo -e "${GREEN}✓ Pod with 5 CPUs correctly blocked by updated quota${NC}"
elif kubectl get pod cpu-exceed-updated-quota -n $ALICE_NAMESPACE > /dev/null 2>&1; then
    echo -e "${RED}✗ Pod with 5 CPUs should be blocked by updated 4 CPU quota${NC}"
    kubectl delete pod cpu-exceed-updated-quota -n $ALICE_NAMESPACE --force --grace-period=0 2>/dev/null || true
    exit 1
else
    echo -e "${GREEN}✓ Updated quota enforcement working correctly${NC}"
fi

echo -e "${GREEN}✓ Updated ResourceQuota enforcement tests passed${NC}"
echo ""

# Step 12: Test ResourceQuota cleanup on class deletion
echo -e "${YELLOW}[12/12] Testing ResourceQuota cleanup on class deletion...${NC}"

# Delete the class
echo "Deleting multi-test-class..."
kubectl delete class multi-test-class
sleep 5

# Verify namespaces (and their ResourceQuotas) were deleted
REMAINING=$(kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class --no-headers 2>/dev/null | wc -l)
if [ "$REMAINING" -eq 0 ]; then
    echo -e "${GREEN}✓ All namespaces (and ResourceQuotas) cleaned up after class deletion${NC}"
else
    echo -e "${RED}✗ $REMAINING namespaces still exist after class deletion${NC}"
    kubectl get namespaces -l nerc.mghpcc.org/class=multi-test-class
    exit 1
fi

# Clean up single namespace class
kubectl delete class test-class
sleep 2
echo ""

# Final summary
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}All ResourceQuota tests passed! ✓${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Test Summary:"
echo "  ✓ ResourceQuotas created in multi-namespace mode"
echo "  ✓ ResourceQuota values match class spec (CPU, Memory, Pods)"
echo "  ✓ ResourceQuota labels are correct"
echo "  ✓ ResourceQuotas NOT created in single-namespace mode (as expected)"
echo "  ✓ ResourceQuota created for dynamically added user"
echo "  ✓ ResourceQuota enforcement blocks pods exceeding CPU limits"
echo "  ✓ ResourceQuota enforcement blocks pods exceeding Memory limits"
echo "  ✓ ResourceQuota enforcement blocks pods exceeding pod count"
echo "  ✓ ResourceQuota allows valid pods within limits"
echo "  ✓ ResourceQuotas updated when class spec changes"
echo "  ✓ Updated quota enforcement validated (new limits enforced)"
echo "  ✓ ResourceQuotas cleaned up on class deletion"
echo ""
echo "Operator logs saved to: /tmp/operator-quota-test.log"
echo ""
