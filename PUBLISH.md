# Publish anyhost-fullstack-smoke

This fixture lives in the AnyHost monorepo until the golden GitHub repository exists.

## Create the public golden repo

```sh
# From a machine with gh auth against anyhostcloud
cd fixtures/anyhost-fullstack-smoke
gh repo create anyhostcloud/anyhost-fullstack-smoke \
  --public \
  --description "Fullstack managed-resource smoke app for AnyHost launch gates" \
  --source . \
  --remote origin \
  --push
```

If the directory is already tracked only inside the monorepo, prefer:

```sh
TMP="$(mktemp -d)"
rsync -a --exclude .git ./fixtures/anyhost-fullstack-smoke/ "$TMP/"
cd "$TMP"
git init -b main
git add .
git commit -m "Initial fullstack smoke app for Fullstack Resource Gate"
gh repo create anyhostcloud/anyhost-fullstack-smoke --public --source . --remote origin --push
```

## GitHub App selection

1. Open the AnyHost GitHub App installation for org `anyhostcloud`.
2. Ensure **selected repositories** include:
   - `anyhost-smoke-test`
   - `anyhost-fullstack-smoke`
3. In Customer Console, confirm the repo appears for import/link and an unselected repo stays hidden.

## First gate run

```sh
# After the public repo exists:
scripts/test-suite.sh --suite fullstack --env staging --repo anyhost-fullstack-smoke --services db,storage,redis
```

The suite enables fullstack component checks (`/db` `/storage` `/redis` `/env`) for this official repo.

## Update Gate Fixture Readiness

After publish + App select + one successful staging fullstack run, update
[`docs/ops/gate-fixture-readiness.md`](../../docs/ops/gate-fixture-readiness.md):

- `anyhost-fullstack-smoke` → Ready
- Fullstack endpoints → Ready
