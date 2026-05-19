#!/bin/bash
set -e

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test configuration
CLUSTER_NAME="class-culler-test"
TEST_TIMEOUT=30

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Class Culler Controller Test Suite${NC}"
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

# Initial cleanup
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
echo -e "${GREEN}✓ Group CRD installed${NC}"
echo ""

# Step 4: Install Kubeflow Notebook CRD
echo -e "${YELLOW}[4/10] Installing Kubeflow Notebook CRD...${NC}"
cat <<EOF | kubectl apply -f -
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: notebooks.kubeflow.org
spec:
  group: kubeflow.org
  names:
    kind: Notebook
    listKind: NotebookList
    plural: notebooks
    singular: notebook
  scope: Namespaced
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
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
          status:
            type: object
            x-kubernetes-preserve-unknown-fields: true
EOF
sleep 2
echo -e "${GREEN}✓ Notebook CRD installed${NC}"
echo ""

# Step 5: Create test groups
echo -e "${YELLOW}[5/10] Creating test groups...${NC}"

# Group 1: CS101 students (alice, bob)
cat <<EOF | kubectl apply -f -
apiVersion: user.openshift.io/v1
kind: Group
metadata:
  name: cs101-students
users:
  - alice
  - bob
EOF

# Group 2: CS202 students (charlie, david)
cat <<EOF | kubectl apply -f -
apiVersion: user.openshift.io/v1
kind: Group
metadata:
  name: cs202-students
users:
  - charlie
  - david
EOF

# Group 3: CS303 students (eve)
cat <<EOF | kubectl apply -f -
apiVersion: user.openshift.io/v1
kind: Group
metadata:
  name: cs303-students
users:
  - eve
EOF

echo -e "${GREEN}✓ Test groups created${NC}"
echo ""

# Step 6: Start operator in background
echo -e "${YELLOW}[6/10] Starting operator...${NC}"
go run main.go > /tmp/operator-culler-test.log 2>&1 &
OPERATOR_PID=$!
sleep 5

# Check if operator is running
if ! ps -p $OPERATOR_PID > /dev/null; then
    echo -e "${RED}✗ Operator failed to start${NC}"
    echo "Log output:"
    cat /tmp/operator-culler-test.log
    exit 1
fi
echo -e "${GREEN}✓ Operator started (PID: $OPERATOR_PID)${NC}"
echo ""

# Step 7: Create test classes with different cutoff times
echo -e "${YELLOW}[7/10] Creating test classes with different cutoffs...${NC}"

# Class 1: CS101 - 10 second cutoff (short)
cat <<EOF | kubectl apply -f -
apiVersion: nerc.mghpcc.org/v1alpha1
kind: Class
metadata:
  name: cs101-class
spec:
  classCode: cs101
  semester: spring2024
  professors:
    - prof-smith
  studentsGroup: cs101-students
  deployment:
    multiNamespace: false
  notebookCulling:
    enabled: true
    cutoff: 10
EOF

# Class 2: CS202 - 20 second cutoff (medium)
cat <<EOF | kubectl apply -f -
apiVersion: nerc.mghpcc.org/v1alpha1
kind: Class
metadata:
  name: cs202-class
spec:
  classCode: cs202
  semester: spring2024
  professors:
    - prof-jones
  studentsGroup: cs202-students
  deployment:
    multiNamespace: false
  notebookCulling:
    enabled: true
    cutoff: 20
EOF

# Class 3: CS303 - culling disabled
cat <<EOF | kubectl apply -f -
apiVersion: nerc.mghpcc.org/v1alpha1
kind: Class
metadata:
  name: cs303-class
spec:
  classCode: cs303
  semester: spring2024
  professors:
    - prof-williams
  studentsGroup: cs303-students
  deployment:
    multiNamespace: false
  notebookCulling:
    enabled: false
    cutoff: 10
EOF

sleep 3

# Wait for class-controller to create namespaces and update status
echo "Waiting for class-controller to create namespaces..."
for i in {1..10}; do
    CS101_NS=$(kubectl get class cs101-class -o jsonpath='{.status.namespaces[0]}' 2>/dev/null || echo "")
    CS202_NS=$(kubectl get class cs202-class -o jsonpath='{.status.namespaces[0]}' 2>/dev/null || echo "")
    CS303_NS=$(kubectl get class cs303-class -o jsonpath='{.status.namespaces[0]}' 2>/dev/null || echo "")

    if [ ! -z "$CS101_NS" ] && [ ! -z "$CS202_NS" ] && [ ! -z "$CS303_NS" ]; then
        echo "All namespaces created:"
        echo "  cs101-class: $CS101_NS"
        echo "  cs202-class: $CS202_NS"
        echo "  cs303-class: $CS303_NS"
        break
    fi
    sleep 2
done

if [ -z "$CS101_NS" ] || [ -z "$CS202_NS" ] || [ -z "$CS303_NS" ]; then
    echo -e "${RED}✗ Class-controller failed to create namespaces${NC}"
    echo "Operator logs:"
    cat /tmp/operator-culler-test.log
    exit 1
fi

sleep 2
echo -e "${GREEN}✓ Test classes created${NC}"
echo ""

# Step 8: Create test notebooks with different ages
echo -e "${YELLOW}[8/10] Creating test notebooks...${NC}"

# Helper function to create notebook
create_notebook() {
    local name=$1
    local namespace=$2
    local username=$3

    cat <<EOF | kubectl apply -f -
apiVersion: kubeflow.org/v1
kind: Notebook
metadata:
  name: ${name}
  namespace: ${namespace}
  annotations:
    opendatahub.io/username: "${username}"
spec:
  template:
    spec:
      containers:
      - name: notebook
        image: jupyter/minimal-notebook:latest
EOF
}

# Create "old" notebooks that will exceed cutoff
echo "Creating old notebooks (Bob, David)..."
create_notebook "jupyter-nb-bob" "$CS101_NS" "bob"           # Will be >10s old
create_notebook "jupyter-nb-david" "$CS202_NS" "david"       # Will be >20s old

# Create unauthorized user notebooks (should be deleted immediately)
create_notebook "jupyter-nb-frank" "$CS101_NS" "frank"       # User not in group - SHOULD be deleted
create_notebook "jupyter-nb-george" "$CS202_NS" "george"     # User not in group - SHOULD be deleted

# Create notebook with culling disabled
create_notebook "jupyter-nb-eve" "$CS303_NS" "eve"           # Culling disabled - should NOT be stopped

sleep 2
echo -e "${GREEN}✓ Old notebooks and unauthorized notebooks created${NC}"

# Wait 12 seconds (so Bob is >10s old, should be stopped)
echo "Waiting 12 seconds for Bob's notebook to exceed cutoff..."
sleep 12

# Now create fresh notebooks
echo "Creating fresh notebooks (Alice, Charlie)..."
create_notebook "jupyter-nb-alice" "$CS101_NS" "alice"       # Fresh - should NOT be stopped
create_notebook "jupyter-nb-charlie" "$CS202_NS" "charlie"   # Fresh - should NOT be stopped

sleep 2
echo -e "${GREEN}✓ All test notebooks created${NC}"
echo ""

# Step 9: Test culler controller behavior
echo -e "${YELLOW}[9/10] Testing culler controller behavior...${NC}"

# Give culler a moment to process
sleep 3

echo -e "\n${BLUE}Test 1: Fresh notebooks within cutoff should remain active${NC}"
# Alice's notebook (fresh, cutoff 10s) - should be active
ALICE_STOPPED=$(kubectl get notebook jupyter-nb-alice -n "$CS101_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ -z "$ALICE_STOPPED" ]; then
    echo -e "${GREEN}✓ Alice's notebook (fresh, cutoff 10s) is still active${NC}"
else
    echo -e "${RED}✗ Alice's notebook was incorrectly stopped${NC}"
    exit 1
fi

# Charlie's notebook (fresh, cutoff 20s) - should be active
CHARLIE_STOPPED=$(kubectl get notebook jupyter-nb-charlie -n "$CS202_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ -z "$CHARLIE_STOPPED" ]; then
    echo -e "${GREEN}✓ Charlie's notebook (fresh, cutoff 20s) is still active${NC}"
else
    echo -e "${RED}✗ Charlie's notebook was incorrectly stopped${NC}"
    exit 1
fi

echo -e "\n${BLUE}Test 2: Notebooks exceeding cutoff should be stopped${NC}"
# Bob's notebook (>12s old, cutoff 10s) - should be stopped
BOB_STOPPED=$(kubectl get notebook jupyter-nb-bob -n "$CS101_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ ! -z "$BOB_STOPPED" ]; then
    echo -e "${GREEN}✓ Bob's notebook (>12s old, cutoff 10s) was stopped${NC}"
else
    echo -e "${RED}✗ Bob's notebook should have been stopped${NC}"
    exit 1
fi

# David's notebook (>12s old, cutoff 20s) - should NOT be stopped yet (within cutoff)
DAVID_STOPPED=$(kubectl get notebook jupyter-nb-david -n "$CS202_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ -z "$DAVID_STOPPED" ]; then
    echo -e "${GREEN}✓ David's notebook (>12s old, cutoff 20s) is still active${NC}"
else
    echo -e "${RED}✗ David's notebook should not be stopped yet (within 20s cutoff)${NC}"
    exit 1
fi

echo -e "\n${BLUE}Test 3: Notebooks from users not in group should be deleted${NC}"
# Frank's notebook - user not in cs101-students group
kubectl get notebook jupyter-nb-frank -n "$CS101_NS" > /dev/null 2>&1
if [ $? -ne 0 ]; then
    echo -e "${GREEN}✓ Frank's notebook (user not in group) was deleted${NC}"
else
    echo -e "${RED}✗ Frank's notebook should have been deleted${NC}"
    exit 1
fi

# George's notebook - user not in cs202-students group
kubectl get notebook jupyter-nb-george -n "$CS202_NS" > /dev/null 2>&1
if [ $? -ne 0 ]; then
    echo -e "${GREEN}✓ George's notebook (user not in group) was deleted${NC}"
else
    echo -e "${RED}✗ George's notebook should have been deleted${NC}"
    exit 1
fi

echo -e "\n${BLUE}Test 4: Culling disabled should keep notebooks active${NC}"
# Eve's notebook (>12s old, but culling disabled for cs303) - should be active
EVE_STOPPED=$(kubectl get notebook jupyter-nb-eve -n "$CS303_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ -z "$EVE_STOPPED" ]; then
    echo -e "${GREEN}✓ Eve's notebook (>12s old, culling disabled) is still active${NC}"
else
    echo -e "${RED}✗ Eve's notebook should not have been stopped (culling disabled)${NC}"
    exit 1
fi

echo -e "\n${BLUE}Test 5: Wait for David's notebook to exceed cutoff${NC}"
# Wait another 10 seconds (total ~22s since David was created)
echo "Waiting 10 more seconds for David's notebook to exceed 20s cutoff..."
sleep 10

# David's notebook should now be stopped (>22s old, cutoff 20s)
DAVID_STOPPED=$(kubectl get notebook jupyter-nb-david -n "$CS202_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ ! -z "$DAVID_STOPPED" ]; then
    echo -e "${GREEN}✓ David's notebook (>22s old, cutoff 20s) was stopped${NC}"
else
    echo -e "${RED}✗ David's notebook should have been stopped${NC}"
    exit 1
fi

echo -e "\n${BLUE}Test 6: Multiple classes with different cutoffs (user in multiple groups)${NC}"
# Add alice to cs202-students group (higher cutoff: 20s vs 10s)
kubectl patch group cs202-students --type=json -p='[{"op":"add","path":"/users/-","value":"alice"}]'
kubectl patch class cs202-class --type=merge --subresource=status -p "{\"status\":{\"namespaces\":[\"$CS101_NS\", \"$CS202_NS\"]}}"

# Create an "old" notebook for alice in CS101 namespace
echo "Creating alice-old notebook..."
create_notebook "jupyter-nb-alice-old" "$CS101_NS" "alice"

# Wait 15 seconds (exceeds 10s cutoff, but not 20s cutoff)
echo "Waiting 15 seconds..."
sleep 15

# Alice's old notebook should NOT be stopped because she's in cs202 with 20s cutoff (higher)
ALICE_OLD_STOPPED=$(kubectl get notebook jupyter-nb-alice-old -n "$CS101_NS" -o jsonpath='{.metadata.annotations.kubeflow-resource-stopped}' 2>/dev/null || echo "")
if [ -z "$ALICE_OLD_STOPPED" ]; then
    echo -e "${GREEN}✓ Alice's old notebook (15s old, in 2 classes) uses higher cutoff (20s) and is active${NC}"
else
    echo -e "${RED}✗ Alice should use the higher cutoff from cs202 (20s), not cs101 (10s)${NC}"
    exit 1
fi

echo ""

# Final summary
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}All culler tests passed! ✓${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Test Summary:"
echo "  ✓ Fresh notebooks within cutoff remain active"
echo "  ✓ Old notebooks exceeding cutoff are stopped"
echo "  ✓ Notebooks from users not in group are deleted"
echo "  ✓ Culling disabled classes are not affected"
echo "  ✓ Time-based culling works correctly (David stopped after 22s)"
echo "  ✓ Multiple classes use highest cutoff for shared users"
echo ""
echo "Operator logs saved to: /tmp/operator-culler-test.log"
echo ""
