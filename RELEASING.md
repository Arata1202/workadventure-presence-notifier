# Releasing

Releases are created from immutable semantic-version tags and publish multi-platform images to GitHub Container Registry.

## Prerequisites

Before creating a release:

- Make the repository public. The release workflow intentionally fails while it is private.
- Confirm that the latest `main` CI run succeeds.
- Complete an integration test against the intended WorkAdventure deployment.
- Review merged changes and choose the next semantic version.
- Confirm that private vulnerability reporting is enabled after the repository becomes public.

## Create a release

Update the local `main` branch, create an annotated tag, and push only that tag:

~~~sh
git switch main
git pull --ff-only origin main
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
~~~

Release tags must use the exact `vMAJOR.MINOR.PATCH` format and point to a commit contained in `main`.

The release workflow will:

1. Run the full CI workflow.
2. Build Linux AMD64 and ARM64 images.
3. Publish version, major/minor, major, and `latest` tags to `ghcr.io/arata1202/workadventure-presence-notifier`.
4. Attach an SBOM and GitHub artifact provenance.
5. Verify that the image is anonymously accessible and contains both target platforms.
6. Create a GitHub Release with generated release notes.

## First GHCR publication

GitHub Container Registry packages are private when first published unless their visibility has already been changed. On the first release, the image publication can succeed while the anonymous-access verification fails.

If that happens:

1. Open the package settings on GitHub.
2. Confirm that the package is linked to this repository.
3. Change the package visibility to **Public**.
4. Re-run only the failed jobs in the release workflow.

The GitHub Release is created only after the public-image verification succeeds.

## Failed releases

If a failure occurs before publication, fix the problem and create a new version tag. Do not move or overwrite a tag that has already published container images.
