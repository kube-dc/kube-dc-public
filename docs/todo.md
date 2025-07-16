# Todo List

- [ ] Implement default NetworkPolicy creation in the Project controller. The controller should create a 'default-deny-all' NetworkPolicy in the project namespace upon project creation. This requires:
  - Adding a `res_network_policy.go` file in `internal/project`.
  - Updating `internal/project/project.go` to call the new function.
  - Rebuilding and deploying the controller image (`make docker-build deploy`).
  - Re-enabling the e2e test in `tests/e2e/project_test.go`.
