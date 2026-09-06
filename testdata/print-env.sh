#!/bin/sh
name=$1
value=$(eval echo "\$$name")
if [ -z "$value" ]; then
  exit 1
fi
printf '%s' "$value"
exit 0
