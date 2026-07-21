# OS Config E2E v2 proof of concept

This directory is an intentionally small implementation of the architecture in `../design.md`. It does not replace the existing E2E suite yet.

The POC demonstrates:

- Standard Go tests and parallel subtests
- Explicit functional, compatibility, and install/upgrade scenario bodies
- Two functional inventory-reporting cases using immutable candidate images
- Two strong compatibility cases using unmodified public images
- One installation and one exact-starting-version upgrade case using fresh public images and exact package digests
- Context-aware project/zone capacity leases
- A unique run and attempt namespace for every resource
- Cleanup registered before resource creation and executed with a fresh timeout
- Context-aware polling with recent observations in timeout errors
- Per-attempt manifests, structured events, inventory, policy, patch, bootstrap, and serial artifacts
- A hard boundary preventing compatibility or installation cases from using candidate images

The functional scenarios validate fresh inventory identity and known installed packages, following the original inventory-reporting suite. Each compatibility scenario installs the exact candidate agent, validates inventory, enforces a minimal OS-policy exec resource and its recorded output, and executes a patch dry run targeted to the exact VM. The POC does not yet implement guest-policy scenarios, the complete OS-policy or patch matrix, the production image-builder pipeline, a TTL janitor, cross-process quota coordination, or CI result conversion.

## Why normal local tests are safe

Cloud scenarios are guarded by the `e2e` build tag. Running the following command does not create GCP resources:

```sh
cd e2e_tests_v2
go test ./...
```

Compile the tagged cloud test without running it using:

```sh
go test -c -tags=e2e -o /tmp/osconfig-e2e-v2.test .
```

## Cloud prerequisites

The tagged tests cannot run successfully without a prepared GCP environment. Before running them, provide:

1. One or more dedicated test projects and zones with sufficient VM, disk, external IP, and OS Config quota.
2. Application Default Credentials for the test operator:

   ```sh
   gcloud auth application-default login
   ```

3. The Compute Engine and OS Config APIs enabled in every test project.
4. A VM service account with permission to run the OS Config agent and report inventory.
5. Test-runner permission to manage VMs and OS policy assignments and to execute/read patch jobs and inventory reports.
6. A default VPC, or change `network` in the run configuration.
7. Network access from the test VMs to Google APIs and the configured package URLs.
8. A dedicated image project containing the two validated candidate images used by the functional POC cases.
9. Read/use permission on those images for every test-project service agent and test runner.

Do not point these tests at a production project. The POC creates and deletes VMs and enables broad debug diagnostics on them.

## Prepare immutable image inputs

Copy the sample manifest:

```sh
cp images.example.json images.local.json
```

Replace every placeholder with a concrete Compute Engine image resource. Do not use image families in `images.local.json`; resolve families before the run so the artifact manifest is reproducible.

The public entries must reference unmodified public images. The candidate entries must be custom images created from the corresponding concrete public images and must contain the exact agent artifact under test.

For this POC, an image maintainer can create each candidate manually:

1. Create a temporary builder VM in a dedicated image-builder project from the concrete public source image.
2. Install diagnostics and the exact candidate agent package.
3. Verify the installed package digest, agent version, and service state.
4. Remove credentials, logs, package caches, SSH material, machine-specific state, and all previous OS Config policy state.
5. Use the supported OS-specific image preparation process. Windows candidates require the supported Windows generalization flow.
6. Stop the builder VM and create an immutable Compute Engine custom image from its boot disk.
7. Boot a validation VM and confirm that the agent reports fresh inventory.
8. Record the concrete source image, candidate image, agent digest, and bootstrap digest in `images.local.json`.
9. Delete the builder and validation VMs.

The production implementation should automate these steps with a dedicated Cloud Build service account and store the resulting Compute Engine custom images in a dedicated image project. Test runners should receive read/use permission only. Agent packages and checksums belong in GCS or Artifact Registry; the VM images themselves belong in the Compute Engine image catalog.

## Prepare the run configuration

Copy the sample configuration:

```sh
cp config.example.json config.local.json
```

Edit:

- `targets`: dedicated project/zone capacity allocations. The in-process scheduler will never exceed their summed capacities.
- `image_manifest`: path to the concrete manifest prepared above.
- `artifact_dir`: where per-attempt diagnostics are written.
- `categories`: a comma-separated subset, or all three as shown.
- `test_timeout`: overall per-case budget; the strong compatibility sequence defaults to 60 minutes.
- `deb_package_url` and `rpm_package_url`: HTTPS URLs reachable from test VMs. Signed URLs are supported but must not be committed.
- `deb_package_sha256` and `rpm_package_sha256`: exact lowercase or uppercase SHA-256 values. Installation stops if a package does not match.
- `deb_expected_version` and `rpm_expected_version`: exact versions reported by `dpkg-query` and `rpm -q --qf '%{VERSION}-%{RELEASE}'` after installation.
- `rpm_upgrade_from_version`: the exact RPM version-release expected on the concrete EL9 source image. The upgrade fails before mutation if it differs.

Do not commit `config.local.json`, signed URLs, credentials, or local artifacts.

The current POC uses Application Default Credentials. If a custom OS Config endpoint is required, set `osconfig_endpoint` in the JSON configuration.

## Run all six cloud scenarios

From this directory:

```sh
E2E_CONFIG="$PWD/config.local.json" \
  go test -tags=e2e -v -count=1 -parallel=6 -timeout=90m .
```

The Go `-parallel` value limits runnable parallel subtests. The target capacities provide the authoritative cloud-resource limit; use both controls.

Run one category:

```sh
E2E_CONFIG="$PWD/config.local.json" \
  go test -tags=e2e -v -count=1 -parallel=3 -timeout=90m \
  -run '^TestFunctionalInventoryReporting$' .
```

Alternatively, set `categories` in `config.local.json`. Disabled categories are reported as skipped rather than silently omitted.

## Artifacts and failure diagnosis

Each attempt writes under:

```text
ARTIFACT_DIR/<stable-test-id>/<attempt-id>/
```

Expected files include:

- `manifest.json`: run, test, category, project, zone, image provenance, and package digest
- `events.jsonl`: phase start, pass, failure, timing, and cleanup events
- `instance.json`: exact VM identity
- `bootstrap.json`: preexisting and installed candidate versions
- `inventory.json` and `inventory-full.json`: summary and full package-level inventory
- `os-policy-assignment.json`, `os-policy-report-last.json`, and `os-policy-report.json`: exact desired state, latest observation, and compliant enforcement result
- `patch-job-last.json`, `patch-job.json`, and `patch-instance-0.json`: latest patch state and targeted per-instance terminal result
- `serial-port-1.log`: serial output collected before deletion

Wait failures include recent observations instead of only saying that a timeout occurred. Installation scripts also publish `osconfig_e2e/bootstrap_status`; an explicit bootstrap failure ends the wait early. The script verifies the downloaded digest, installed version, and active/enabled service before publishing readiness.

Cleanup uses an independent context. If deletion fails, the test remains failed and the resource identity is preserved in its manifest and events. The POC does not include the production TTL janitor, so inspect the test projects after experimental runs and remove any labeled `e2e-run` resources that outlive a failed process.

## Expected limitations

- A developer laptop can compile and unit-test the harness, but cannot fully validate cloud behavior without the projects, images, artifacts, IAM, APIs, and quota above.
- The Debian installation and EL upgrade scripts are representative, not a complete packaging matrix.
- The patch check is intentionally a dry run. It proves exact targeting, agent task execution, and successful per-instance reporting without making package state dependent on the day the image is tested.
- The OS-policy check covers one Linux exec resource. Package, file, repository, Windows, and audit-mode policy resources remain outside the POC.
- The scheduler coordinates one test process. CI shards must receive disjoint projects or target allocations until a cross-process lease service exists.
