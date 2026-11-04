#!/bin/sh
# Local argv/metadata fixture only: no VM, nerdctl, systemctl or iptables runs.
set -eu
printf '%s\n' "$*" >> "$YOLOCTL_TEST_FORWARD_DIR/commands.log"
[ "$1" = shell ] || exit 97
shift
case "$1" in yolo-*) ;; *) exit 97 ;; esac
shift
[ "$1" = -- ] || exit 97
shift
[ "$1" = sudo ] || exit 97
shift
case "$1" in
  systemctl)
    shift
    case "$1" in
      list-units)
        count=0
        if [ -f "$YOLOCTL_TEST_FORWARD_DIR/list-count" ]; then
          count=$(cat "$YOLOCTL_TEST_FORWARD_DIR/list-count")
        fi
        count=$((count + 1))
        printf '%s\n' "$count" > "$YOLOCTL_TEST_FORWARD_DIR/list-count"
        if [ "$count" = "${YOLOCTL_TEST_FORWARD_LIST_FAIL_AT:-never}" ]; then
          exit 24
        fi
        cat "$YOLOCTL_TEST_FORWARD_DIR/units"
        exit "${YOLOCTL_TEST_FORWARD_LIST_EXIT:-0}" ;;
      stop)
        case "$2" in
          yoloctl-openshell-port-*) exit "${YOLOCTL_TEST_FORWARD_STOP_EXIT:-0}" ;;
          yoloctl-port-*) exit 0 ;;
          *) exit 98 ;;
        esac ;;
      is-active) printf 'inactive\n'; exit 3 ;;
      *) exit 98 ;;
    esac ;;
  nerdctl)
    shift
    case "$1" in
      ps) cat "$YOLOCTL_TEST_FORWARD_DIR/containers.json" ;;
      inspect) cat "$YOLOCTL_TEST_FORWARD_DIR/inspect.json" ;;
      exec)
        [ "$2" = web ] || exit 98
        [ "$3" = iptables ] || exit 98
        exit 0 ;;
      *) exit 98 ;;
    esac ;;
  *) exit 98 ;;
esac
