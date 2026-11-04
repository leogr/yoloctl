#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$YOLOCTL_TEST_SERVICE_LOG"
[ "$1" = shell ] || exit 97
shift
case "$1" in yolo-*) ;; *) exit 97 ;; esac
shift
[ "$1" = -- ] || exit 97
shift
[ "$1" = sudo ] || exit 97
shift
case "$1" in
  systemd-run) exit 0 ;;
  systemctl) ;;
  *) exit 97 ;;
esac
shift
case "$1" in
  stop) exit "$YOLOCTL_TEST_STOP_EXIT" ;;
  is-active) printf '%s\n' "$YOLOCTL_TEST_SERVICE_STATE"; exit 3 ;;
  *) exit 98 ;;
esac
