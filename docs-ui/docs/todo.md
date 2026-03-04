# Todo List

- [ ] Implement default NetworkPolicy creation in the Project controller. The controller should create a 'default-deny-all' NetworkPolicy in the project namespace upon project creation. This requires:
  - Adding a `res_network_policy.go` file in `internal/project`.
  - Updating `internal/project/project.go` to call the new function.
  - Rebuilding and deploying the controller image (`make docker-build deploy`).
  - Re-enabling the e2e test in `tests/e2e/project_test.go`.

- [ ] **Fix Project Controller Deletion Deadlock**
  - **Issue**: The `Project` controller gets stuck in a deadlock when deleting a `Project` that has an empty `spec.egressNetworkType`. The deletion logic in `internal/project/res_vpc.go` incorrectly attempts to re-generate the `kube-ovn` VPC if it's not found, which fails because `utils.SelectBestExternalSubnet` requires an `egressNetworkType`.
  - **Fix**: In `internal/project/res_vpc.go`, inside the `NewProjectVpc` function, modify the `IsNotFound` error handling. Before attempting to regenerate the VPC, add a check to see if the project has a `DeletionTimestamp`. If it does, the function should return the `NotFound` error directly, as this is an expected condition during cleanup, and regeneration should not occur.
