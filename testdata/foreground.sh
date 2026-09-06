status_file=$HIVEDECK_STATUS_FILE
exit_code=$1
event=$2
printf '{"event":"%s","artifact":"artifact.html"}\n' "$event" >> "$status_file"
exit "$exit_code"
