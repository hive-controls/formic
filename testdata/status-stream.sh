status_file=$HIVEDECK_STATUS_FILE
gate_file=$1
printf '{"event":"started"}\n' >> "$status_file"
printf '{"event":"progress","artifact":"evidence.html"}\n' >> "$status_file"
while [ ! -f "$gate_file" ]; do
  sleep 0.02
done
printf '{"event":"ok","artifact":"evidence.html"}\n' >> "$status_file"
exit 0
