#!/bin/sh
# ESP helper on a raw disk image: esp.sh <img> ls|put|rm|cat ...   (ESP starts at sector 2048 = 1 MiB)
set -eu
img=$1; op=$2; shift 2
export MTOOLS_SKIP_CHECK=1
case $op in
  ls)  mdir -/ -b -i "$img@@1048576" ::/ ;;
  put) mmd -D s -i "$img@@1048576" "$(dirname "::$2")" 2>/dev/null || true; mcopy -D o -i "$img@@1048576" "$1" "::$2" ;;
  rm)  mdel -i "$img@@1048576" "::$1" ;;
  mv)  mren -i "$img@@1048576" "::$1" "::$2" ;;
  cat) mtype -i "$img@@1048576" "::$1" ;;
esac
