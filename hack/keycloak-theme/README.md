# Keycloak source moved

All login, native account, email, logo, appearance, packaging and test sources now live in [kube-dc/ui](https://github.com/kube-dc/ui/tree/main/keycloak).

`build-theme.sh` forwards to that checkout (`KUBE_DC_UI_DIR`, default sibling `../kube-dc-ui`). Install the UI build prerequisites first. Output is in the UI checkout's `keycloak/dist/`, or pass `--output` explicitly. Do not recreate theme source here or patch generated JARs.

Use `kube-dc-theme.jar` with the Fleet extraction init container; `kube-dc-theme-provider.jar` is for the providers installation layout. They are not interchangeable. The UI release manifest records both checksums. `cicd/release/prepare-ui.py` validates the artifacts and generates a cluster-scoped Fleet proposal without deployment.

The old email-only repackager and global realm mutation script are retired. Keep realm selection and the current `kube-dc` theme name under platform configuration. For the temporary stage preview, follow the UI theme README and its journaled stage tool. After Fleet adoption, never restore an obsolete preview journal over the release.
