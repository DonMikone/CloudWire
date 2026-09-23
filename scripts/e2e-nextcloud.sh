#!/usr/bin/env bash
# End-to-end test of the CloudWire Core against a throwaway Nextcloud in Docker.
#
# Runs an isolated Core (CLOUDWIRE_HOME in a temp dir, separate Keychain
# service) and drives it through `cloudwire-core rpc`. The real user state is
# never touched. Requires Docker, curl and jq. E2E_KEEP=1 keeps the state for
# debugging.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${CORE_BIN:-$ROOT/build/core/cloudwire-core}"
NC_NAME="cw-e2e-nc"
NC_PORT="${NC_PORT:-8089}"
NC_URL="http://localhost:$NC_PORT"
ADMIN_PASS="cw-e2e-pass"
BOB_PASS="cw-e2e-bob-Pass-42"
VAULT_PASS="e2e vault password"
KC_SERVICE="io.github.donmikone.cloudwire.e2e"

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker is not running" >&2; exit 1; }
[ -x "$BIN" ] || "$ROOT/scripts/build-core.sh" debug native

# Short path: the unix socket path must stay below 104 bytes.
CW_HOME="$(mktemp -d /tmp/cw-e2e.XXXXXX)"
export CLOUDWIRE_HOME="$CW_HOME" CLOUDWIRE_KEYCHAIN_SERVICE="$KC_SERVICE"
CORE_PID=""
EVENTS_PID=""
PASS=0

cleanup() {
  set +e
  [ -n "$EVENTS_PID" ] && kill "$EVENTS_PID" 2>/dev/null
  if [ -n "$CORE_PID" ] && kill -0 "$CORE_PID" 2>/dev/null; then
    "$BIN" rpc core.shutdown >/dev/null 2>&1
    for _ in $(seq 1 30); do kill -0 "$CORE_PID" 2>/dev/null || break; sleep 1; done
    kill -9 "$CORE_PID" 2>/dev/null
  fi
  mount | awk -v h="$CW_HOME" 'index($0, h) {print $3}' | while read -r mp; do diskutil unmount force "$mp" >/dev/null 2>&1; done
  if [ "${E2E_KEEP:-0}" = 1 ]; then
    echo "E2E_KEEP=1: kept $CW_HOME and the $NC_NAME container" >&2
    return
  fi
  docker rm -f "$NC_NAME" >/dev/null 2>&1
  rm -rf "$CW_HOME"
  while security delete-generic-password -s "$KC_SERVICE" >/dev/null 2>&1; do :; done
  while security delete-generic-password -s "$KC_SERVICE.vault" >/dev/null 2>&1; do :; done
}
trap cleanup EXIT

log() { printf '\n==> %s\n' "$*"; }
ok() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*" >&2; [ -f "$CW_HOME/Library/Logs/CloudWire/core.log" ] && tail -40 "$CW_HOME/Library/Logs/CloudWire/core.log" >&2; exit 1; }
rpc() { "$BIN" rpc "$@"; }
# wait_for <seconds> <description> <command...>
wait_for() {
  local secs=$1 desc=$2; shift 2
  for _ in $(seq 1 "$secs"); do
    if "$@" >/dev/null 2>&1; then ok "$desc"; return 0; fi
    sleep 1
  done
  fail "$desc (timed out after ${secs}s)"
}
dav() { curl -fsS -u "admin:$ADMIN_PASS" "$@"; }
dav_url() { printf '%s/remote.php/dav/files/admin/%s' "$NC_URL" "$1"; }
exists_remote() { curl -fsS -o /dev/null -u "admin:$ADMIN_PASS" -X PROPFIND -H 'Depth: 0' "$(dav_url "$1")"; }
missing_remote() { ! exists_remote "$1"; }

log "Starting Nextcloud ($NC_NAME on port $NC_PORT)"
docker rm -f "$NC_NAME" >/dev/null 2>&1 || true
docker run -d --name "$NC_NAME" -p "$NC_PORT:80" -e SQLITE_DATABASE=nc -e NEXTCLOUD_ADMIN_USER=admin \
  -e NEXTCLOUD_ADMIN_PASSWORD="$ADMIN_PASS" -e NEXTCLOUD_TRUSTED_DOMAINS=localhost nextcloud:stable >/dev/null
installed() { curl -fsS "$NC_URL/status.php" | jq -e '.installed == true'; }
wait_for 300 "Nextcloud installed" installed
docker exec -e OC_PASS="$BOB_PASS" -u www-data "$NC_NAME" php occ user:add --password-from-env bob >/dev/null
ok "user bob created"
APP_PASS="$(curl -fsS -u "admin:$ADMIN_PASS" -H 'OCS-APIRequest: true' "$NC_URL/ocs/v2.php/core/getapppassword?format=json" | jq -r '.ocs.data.apppassword')"
[ -n "$APP_PASS" ] && [ "$APP_PASS" != null ] && ok "app password issued" || fail "no app password"

log "Seeding cloud files"
dav -X MKCOL "$(dav_url Music)" >/dev/null
dav -X MKCOL "$(dav_url Music/Loops)" >/dev/null
dav -X MKCOL "$(dav_url Bulk)" >/dev/null
dav -X MKCOL "$(dav_url Secret)" >/dev/null
printf 'mix v1' | dav -T - "$(dav_url Music/Mix.wav)"
printf 'loop' | dav -T - "$(dav_url Music/Loops/loop1.wav)"
for i in $(seq 0 9); do printf "bulk $i" | dav -T - "$(dav_url "Bulk/f$i.wav")"; done
printf 'the secret plan' | dav -T - "$(dav_url Secret/plan.txt)"
dav -X MKCOL "$(dav_url Samples)" >/dev/null
printf 'kick' | dav -T - "$(dav_url Samples/kick.wav)"
printf 'snare' | dav -T - "$(dav_url Samples/snare.wav)"
ok "cloud seeded"

log "Starting the isolated Core in $CW_HOME"
"$BIN" serve --force &
CORE_PID=$!
wait_for 30 "Core socket up" rpc core.info
"$BIN" events >"$CW_HOME/events.jsonl" &
EVENTS_PID=$!
rpc settings.update '{"partial":{"quietPeriodSeconds":2,"nextcloudEtagSeconds":10,"pauseRules":{"battery":{"enabled":false},"meteredNetwork":{"enabled":false},"cpu":{"enabled":false},"studioMode":{"enabled":false}}}}' >/dev/null
ok "settings updated for the test"

log "Connection"
CID="$(rpc connections.nextcloudManual "{\"serverURL\":\"$NC_URL\",\"user\":\"admin\",\"appPassword\":\"$APP_PASS\",\"name\":\"E2E Cloud\"}" | jq -r .id)"
[ -n "$CID" ] && ok "connection $CID" || fail "connection"
rpc connections.test "{\"id\":\"$CID\"}" | jq -e '.ok == true' >/dev/null && ok "connection test" || fail "connection test"
rpc connections.browse "{\"connectionId\":\"$CID\",\"path\":\"Music\"}" | jq -e '.[0].name == "Loops" and .[0].isDir and (map(.name) | index("Mix.wav"))' >/dev/null \
  && ok "browse lists folders first" || fail "browse"
if rpc connections.nextcloudManual "{\"serverURL\":\"$NC_URL\",\"user\":\"admin\",\"appPassword\":\"wrong\",\"name\":\"Bad\"}" >/dev/null 2>&1; then
  fail "wrong app password accepted"
else ok "wrong app password rejected"; fi

log "Mount"
MID="$(rpc mounts.create "{\"connectionId\":\"$CID\",\"remotePath\":\"Music\",\"volumeName\":\"E2E Music\"}" | jq -r .id)"
MP="$(rpc mounts.list | jq -r ".[] | select(.id==\"$MID\") | .mountPoint")"
mounted() { rpc mounts.list | jq -e ".[] | select(.id==\"$MID\") | .state == \"mounted\"" && mount | grep -q "$(basename "$MP")"; }
wait_for 30 "Mount attached at $MP" mounted
rpc mounts.stats "{\"id\":\"$MID\"}" | jq -e 'type == "object"' >/dev/null && ok "VFS stats" || fail "stats"
OLD_WORKER="$(pgrep -f "$BIN worker" | head -1)"
kill -9 "$OLD_WORKER"
reconnected() { mounted && [ -n "$(pgrep -f "$BIN worker")" ] && [ "$(pgrep -f "$BIN worker" | head -1)" != "$OLD_WORKER" ]; }
wait_for 15 "Mount back within 15 s after the worker was killed" reconnected
rpc mounts.unmount "{\"id\":\"$MID\"}" >/dev/null
not_mounted() { ! mount | grep -q "$(basename "$MP")"; }
wait_for 20 "Mount detached" not_mounted

log "Offline Item (folder)"
OID="$(rpc offline.create "{\"connectionId\":\"$CID\",\"kind\":\"folder\",\"remotePath\":\"Music\"}" | jq -r .id)"
SP="$(rpc offline.list | jq -r ".[] | select(.id==\"$OID\") | .storagePath")"
synced() { rpc offline.list | jq -e ".[] | select(.id==\"$OID\") | .state == \"idle\" and .lastSyncAt != null"; }
wait_for 60 "initial sync" synced
[ "$(cat "$SP/Mix.wav")" = "mix v1" ] && [ -f "$SP/Loops/loop1.wav" ] && ok "files are local in $SP" || fail "local files"
printf 'made locally' >"$SP/local-new.wav"
remote_has_local() { dav "$(dav_url Music/local-new.wav)" | grep -q 'made locally'; }
wait_for 60 "local change uploaded after the Quiet Period" remote_has_local
printf 'from the web' | dav -T - "$(dav_url Music/cloud-new.wav)"
local_has_remote() { [ "$(cat "$SP/cloud-new.wav" 2>/dev/null)" = "from the web" ]; }
wait_for 60 "remote change downloaded via ETag check" local_has_remote

LABEL="$(rpc settings.get | jq -r .conflictLabel)"
wait_for 30 "idle before conflict test" synced
printf 'local edit!' >"$SP/Mix.wav"
touch -t 202601010101 "$SP/Mix.wav"
printf 'cloud edit!!' | dav -T - "$(dav_url Music/Mix.wav)"
rpc offline.syncNow "{\"id\":\"$OID\"}" >/dev/null
conflict_local() { ls "$SP" | grep -Eq "^Mix\.$LABEL [0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{4}\.wav$"; }
wait_for 60 "conflict copy created locally ($LABEL)" conflict_local
[ "$(cat "$SP/Mix.wav")" = "local edit!" ] && ok "local version keeps the name" || fail "local version replaced"
CONFLICT="$(ls "$SP" | grep -E "^Mix\.$LABEL ")"
[ "$(cat "$SP/$CONFLICT")" = "cloud edit!!" ] && ok "conflict copy holds the cloud version" || fail "conflict content"
conflict_remote() { exists_remote "Music/$(jq -rn --arg s "$CONFLICT" '$s|@uri')"; }
wait_for 60 "conflict copy uploaded" conflict_remote
rpc offline.statusForPaths "{\"paths\":[\"$SP/$CONFLICT\",\"$SP/Loops/loop1.wav\"]}" | jq -e ".statuses[\"$SP/$CONFLICT\"] == \"conflict\" and .statuses[\"$SP/Loops/loop1.wav\"] == \"synced\"" >/dev/null \
  && ok "badge states" || fail "badge states"

log "Mass-Delete Guard"
BID="$(rpc offline.create "{\"connectionId\":\"$CID\",\"kind\":\"folder\",\"remotePath\":\"Bulk\"}" | jq -r .id)"
BP="$(rpc offline.list | jq -r ".[] | select(.id==\"$BID\") | .storagePath")"
bulk_synced() { rpc offline.list | jq -e ".[] | select(.id==\"$BID\") | .state == \"idle\""; }
wait_for 60 "bulk item synced" bulk_synced
rm "$BP"/f0.wav "$BP"/f1.wav "$BP"/f2.wav "$BP"/f3.wav "$BP"/f4.wav "$BP"/f5.wav
guard() { rpc offline.list | jq -e ".[] | select(.id==\"$BID\") | .state == \"needsConfirmation\""; }
wait_for 60 "guard stopped the run" guard
exists_remote Bulk/f0.wav && ok "cloud files untouched" || fail "cloud files deleted"
rpc offline.confirmMassDelete "{\"id\":\"$BID\",\"action\":\"restore\"}" >/dev/null
restored() { [ "$(ls "$BP" | wc -l | tr -d ' ')" = 10 ]; }
wait_for 60 "\"Nicht löschen\" restored the files" restored

log "Files item"
OVERLAP="$(rpc offline.create "{\"connectionId\":\"$CID\",\"kind\":\"files\",\"remotePath\":\"Music/Loops\",\"files\":[\"loop1.wav\"]}" 2>&1 || true)"
if echo "$OVERLAP" | grep -q offline.overlap; then
  ok "files inside a folder item rejected (overlap)"
else fail "overlap not detected"; fi
FID="$(rpc offline.create "{\"connectionId\":\"$CID\",\"kind\":\"files\",\"remotePath\":\"Samples\",\"files\":[\"kick.wav\"]}" | jq -r .id)"
FP="$(rpc offline.list | jq -r ".[] | select(.id==\"$FID\") | .storagePath")"
files_synced() { [ -f "$FP/kick.wav" ] && [ ! -e "$FP/snare.wav" ]; }
wait_for 60 "files item syncs only the listed file" files_synced
rpc offline.create "{\"connectionId\":\"$CID\",\"kind\":\"files\",\"remotePath\":\"Samples\",\"files\":[\"snare.wav\"]}" | jq -e ".id == \"$FID\" and (.files | length) == 2" >/dev/null \
  && ok "second file merged into the files item" || fail "files merge"
snare_synced() { [ -f "$FP/snare.wav" ]; }
wait_for 60 "merged file synced" snare_synced

log "Shares"
rpc shares.capabilities "{\"connectionId\":\"$CID\"}" | jq -e '.publicLink and .internalLink and .userShare and .emailShare and .manage' >/dev/null && ok "capabilities" || fail "capabilities"
rpc shares.policy "{\"connectionId\":\"$CID\"}" | jq -e 'has("passwordEnforced")' >/dev/null && ok "policy" || fail "policy"
EXP="$(date -v+7d +%Y-%m-%d)"
PUB="$(rpc shares.create "{\"connectionId\":\"$CID\",\"path\":\"Music/Mix.wav\",\"kind\":\"publicLink\",\"password\":\"Share-Pass-123!\",\"expireDate\":\"$EXP\",\"permissions\":1,\"label\":\"E2E\"}")"
echo "$PUB" | jq -e ".hasPassword and .expireDate == \"$EXP\" and (.url | test(\"/s/\"))" >/dev/null && ok "public link with password and expiry" || fail "public link: $PUB"
curl -fsS -o /dev/null "$(echo "$PUB" | jq -r .url)" && ok "public link reachable" || fail "public link unreachable"
rpc shares.internalLink "{\"connectionId\":\"$CID\",\"path\":\"Music/Mix.wav\"}" | jq -e '.url | test("/f/[0-9]+$")' >/dev/null && ok "internal link" || fail "internal link"
rpc shares.searchSharees "{\"connectionId\":\"$CID\",\"search\":\"bob\",\"itemType\":\"folder\"}" | jq -e 'map(.shareWith) | index("bob")' >/dev/null && ok "sharee search" || fail "sharee search"
rpc shares.create "{\"connectionId\":\"$CID\",\"path\":\"Music\",\"kind\":\"user\",\"shareWith\":\"bob\",\"permissions\":1}" | jq -e '.kind == "user"' >/dev/null && ok "user share with bob" || fail "user share"
bob_sees() { curl -fsS -o /dev/null -u "bob:$BOB_PASS" -X PROPFIND -H 'Depth: 0' "$NC_URL/remote.php/dav/files/bob/Music"; }
wait_for 20 "bob sees the shared folder" bob_sees
rpc shares.create "{\"connectionId\":\"$CID\",\"path\":\"Music/Loops\",\"kind\":\"email\",\"shareWith\":\"e2e@example.com\",\"sendMail\":false}" | jq -e '.kind == "email"' >/dev/null && ok "email share" || fail "email share"
C1="$(rpc shares.copyPublicLink "{\"connectionId\":\"$CID\",\"path\":\"Music/Loops\"}")"
C2="$(rpc shares.copyPublicLink "{\"connectionId\":\"$CID\",\"path\":\"Music/Loops\"}")"
[ "$(echo "$C1" | jq -r .created)" = true ] && [ "$(echo "$C2" | jq -r .created)" = false ] && [ "$(echo "$C1" | jq -r .url)" = "$(echo "$C2" | jq -r .url)" ] \
  && ok "copyPublicLink reuses the link" || fail "copyPublicLink: $C1 / $C2"
SHARES="$(rpc shares.list "{\"connectionId\":\"$CID\",\"path\":\"Music/Mix.wav\"}")"
SID="$(echo "$SHARES" | jq -r '.[] | select(.kind=="publicLink") | .id')"
rpc shares.update "{\"connectionId\":\"$CID\",\"id\":\"$SID\",\"label\":\"renamed\"}" | jq -e '.label == "renamed"' >/dev/null && ok "share updated" || fail "share update"
rpc shares.delete "{\"connectionId\":\"$CID\",\"id\":\"$SID\"}" >/dev/null
rpc shares.list "{\"connectionId\":\"$CID\",\"path\":\"Music/Mix.wav\"}" | jq -e 'map(select(.kind=="publicLink")) | length == 0' >/dev/null && ok "share deleted" || fail "share delete"
rpc shares.webURL "{\"connectionId\":\"$CID\",\"path\":\"Music\"}" | jq -e '.url | test("apps/files/\\?dir=%2FMusic")' >/dev/null && ok "web URL" || fail "web URL"
rpc paths.resolve "{\"localPath\":\"$SP/Loops/loop1.wav\"}" | jq -e ".connectionId == \"$CID\" and .remotePath == \"Music/Loops/loop1.wav\" and .context == \"offline\"" >/dev/null && ok "paths.resolve" || fail "paths.resolve"
rpc finder.roots | jq -e '.token != "" and (.roots | length) >= 3' >/dev/null && ok "finder.roots" || fail "finder.roots"

log "Vault"
VR="$(rpc vaults.create "{\"connectionId\":\"$CID\",\"parentPath\":\"\",\"name\":\"E2EVault\",\"password\":\"$VAULT_PASS\",\"unlockMode\":\"keychain\"}")"
VID="$(echo "$VR" | jq -r .vault.id)"
echo "$VR" | jq -e '.recoveryKey | test("^([A-Z2-7]{4}-){12}[A-Z2-7]{4}$")' >/dev/null && ok "vault created with Recovery Key" || fail "vault create: $VR"
exists_remote E2EVault.cwvault/vault.json && ok "vault.json in the cloud" || fail "vault.json missing"
JOB="$(rpc vaults.encryptExisting "{\"connectionId\":\"$CID\",\"path\":\"Secret\",\"isDir\":true,\"target\":{\"vaultId\":\"$VID\",\"subPath\":\"\"},\"ignorePauseRules\":true}" | jq -r .jobId)"
verified() { grep "\"jobId\":\"$JOB\"" "$CW_HOME/events.jsonl" | grep -q '"status":"verified"'; }
wait_for 120 "encrypted copy verified" verified
exists_remote Secret/plan.txt && ok "original kept until confirmed" || fail "original deleted early"
rpc vaults.migrationConfirmDelete "{\"jobId\":\"$JOB\"}" >/dev/null
wait_for 20 "original deleted after confirmation" missing_remote Secret
LISTING="$(curl -fsS -u "admin:$ADMIN_PASS" -X PROPFIND -H 'Depth: infinity' "$(dav_url E2EVault.cwvault/d)")"
echo "$LISTING" | grep -q 'plan.txt' && fail "plaintext name in the vault" || ok "names are encrypted in the cloud"
rpc vaults.lock "{\"id\":\"$VID\"}" | jq -e '.unlocked == false' >/dev/null && ok "vault locked" || fail "lock"
rpc vaults.remove "{\"id\":\"$VID\"}" >/dev/null && ok "vault forgotten locally" || fail "remove"
if rpc vaults.open "{\"connectionId\":\"$CID\",\"vaultPath\":\"E2EVault.cwvault\",\"password\":\"wrong password!\",\"unlockMode\":\"ask\"}" >/dev/null 2>&1; then
  fail "wrong vault password accepted"
else ok "wrong vault password rejected"; fi
V2="$(rpc vaults.open "{\"connectionId\":\"$CID\",\"vaultPath\":\"E2EVault.cwvault\",\"password\":\"$VAULT_PASS\",\"unlockMode\":\"ask\"}")"
VCONN="$(echo "$V2" | jq -r .vaultConnectionId)"
[ "$(echo "$V2" | jq -r .unlocked)" = true ] && ok "vault reopened with its password" || fail "open: $V2"
rpc shares.capabilities "{\"connectionId\":\"$VCONN\"}" | jq -e '.publicLink == false and .reason == "vault"' >/dev/null && ok "sharing disabled inside the vault" || fail "vault sharing"
VOID="$(rpc offline.create "{\"connectionId\":\"$VCONN\",\"kind\":\"folder\",\"remotePath\":\"Secret\"}" | jq -r .id)"
VSP="$(rpc offline.list | jq -r ".[] | select(.id==\"$VOID\") | .storagePath")"
decrypted() { [ "$(cat "$VSP/plan.txt" 2>/dev/null)" = "the secret plan" ]; }
wait_for 60 "decrypted Offline Item readable" decrypted

log "Activity Log"
rpc activity.query '{"levels":["error"],"limit":50}' | jq -r '.[].message' | sed 's/^/  error: /' || true
rpc activity.export "{\"format\":\"csv\",\"path\":\"$CW_HOME/activity.csv\"}" | jq -e '.count > 10' >/dev/null && head -1 "$CW_HOME/activity.csv" | grep -q '^ts,level,category,subject,message' \
  && ok "activity export (CSV)" || fail "activity export"

log "Shutdown"
rpc core.shutdown >/dev/null
stopped() { ! kill -0 "$CORE_PID" 2>/dev/null; }
wait_for 60 "Core stopped" stopped
[ ! -e "$CW_HOME/Library/Application Support/CloudWire/core.sock" ] && ok "socket removed" || fail "socket left behind"
CORE_PID=""

printf '\nAll %d checks passed.\n' "$PASS"
