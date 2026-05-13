
## Automated Testing

### Full Test Suite

Run the complete automated test suite:

```bash
./test-operator.sh cleanup
```

This script will:
1. Create a fresh kind cluster
2. Install all required CRDs
3. Create test resources (groups, classes)
4. Start the operator
5. Test single-namespace class creation
6. Test multi-namespace class creation
7. Verify hash suffixes on namespaces
8. Test dynamic user addition and reconciliation
9. Test automatic cleanup on class deletion
10. Clean up all resources

**Expected output:**
```
========================================
Class Operator Test Suite
========================================

[1/9] Creating kind cluster...
✓ Cluster created

[2/9] Installing Class CRD...
✓ CRD installed

[3/9] Installing OpenShift Group CRD...
✓ Group CRD installed

[4/9] Creating test OpenShift group...
✓ Test group created

[5/9] Starting operator...
✓ Operator started (PID: XXXXX)

[6/9] Testing single-namespace class creation...
✓ Single-namespace created: test101-spring2026
✓ Namespace labels verified
✓ Finalizer added to class

[7/9] Testing multi-namespace class creation...
✓ Multi-namespaces created: 4 namespaces
✓ Namespaces have hash suffix

[8/9] Testing namespace cleanup on class deletion...
✓ Multi-namespace class namespaces cleaned up
✓ Single-namespace class namespace cleaned up

========================================
All tests passed! ✓
========================================
```

