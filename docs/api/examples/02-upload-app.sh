#!/usr/bin/env bash
# Uploads an application folder: deploy.sh becomes the script, every other file
# in the folder a files part. Uploading the same name again adds a version.
#
#   ./02-upload-app.sh [folder] [name] [ports]
#
#   folder  default examples/apps/open-webui of this repository
#   name    default the folder name
#   ports   TCP ports the app serves, comma separated. Default 3000 for
#           open-webui, 7860 for langflow, 80 for dify, none otherwise
set -euo pipefail
. "$(dirname "$0")/common.sh"

repo_root=$(cd "$(dirname "$0")/../../.." && pwd)
dir="${1:-$repo_root/examples/apps/open-webui}"
name="${2:-$(basename "$dir")}"
case "$name" in
open-webui) default_ports=3000 ;;
langflow) default_ports=7860 ;;
dify) default_ports=80 ;;
*) default_ports= ;;
esac
ports="${3:-$default_ports}"

if [ ! -f "$dir/deploy.sh" ]; then
	echo "$dir/deploy.sh not found: the folder needs a deploy.sh to run on the node" >&2
	exit 1
fi

form=(-F "name=$name" -F "script=@$dir/deploy.sh")
if [ -n "$ports" ]; then
	form+=(-F "ports=$ports")
fi
for file in "$dir"/*; do
	if [ -f "$file" ] && [ "$(basename "$file")" != deploy.sh ]; then
		form+=(-F "files=@$file")
	fi
done

aiapp POST /apps "${form[@]}" | jq '{version, ports, files: [.files[] | "\(.name) \(.sizeBytes)B"]}'
