# shellcheck shell=bash
# Runs the TestAcc* suite against a throwaway VM. Extra arguments are passed
# to `go test`, e.g. `test-acc -run TestAccRollback`.

run-vm
trap stop-vm EXIT

export TF_ACC=1 CGO_ENABLED=0
export OPENWRT_ENDPOINT=http://127.0.0.1:${OPENWRT_VM_PORT:-18080}
export OPENWRT_USERNAME=root OPENWRT_PASSWORD=
go test ./internal/provider/ -run TestAcc -count=1 -timeout 30m -v "$@"
