#!/bin/sh
set -eu

data_dir=/data
device_file="${data_dir}/device-id"
mkdir -p "${data_dir}" "${HOME}" "${XDG_CACHE_HOME:-${HOME}/.cache}" "${XDG_CONFIG_HOME:-${HOME}/.config}"

if [ -n "${SODA_DEVICE_ID:-}" ]; then
  device_id=${SODA_DEVICE_ID}
elif [ -s "${device_file}" ]; then
  device_id=$(tr -cd '0-9' <"${device_file}" | cut -c1-20)
else
  device_id=$(date +%s%N | cut -c1-19)
  temp_device_file="${device_file}.tmp"
  printf '%s\n' "${device_id}" >"${temp_device_file}"
  mv "${temp_device_file}" "${device_file}"
fi

case "${device_id}" in
  ''|*[!0-9]*)
    echo "soda-bdms-signer: SODA_DEVICE_ID must contain only digits" >&2
    exit 1
    ;;
esac

if [ "${#device_id}" -lt 16 ] || [ "${#device_id}" -gt 20 ]; then
  echo "soda-bdms-signer: SODA_DEVICE_ID must be 16-20 digits" >&2
  exit 1
fi

export SODA_DEVICE_ID=${device_id}

if [ -x /usr/bin/wine64 ]; then
  wine_bin=/usr/bin/wine64
elif [ -x /usr/lib/wine/wine64 ]; then
  wine_bin=/usr/lib/wine/wine64
else
  echo "soda-bdms-signer: wine64 executable not found" >&2
  exit 1
fi

exec "${wine_bin}" /opt/windows-node/node.exe /app/server.js
