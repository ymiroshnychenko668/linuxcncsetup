#!/bin/bash

set -e

# Collect executable scripts in this directory excluding setup.sh
shopt -s nullglob
scripts=()
for file in *.sh; do
  [ "$file" = "setup.sh" ] && continue
  scripts+=("$file")
done

if [ ${#scripts[@]} -eq 0 ]; then
  echo "No scripts available."
  exit 0
fi

PS3="Select a script to execute (or choose Quit to exit): "
select script in "${scripts[@]}" "Quit"; do
  case $script in
    "Quit")
      echo "Exiting."
      break
      ;;
    "")
      echo "Invalid selection."
      ;;
    *)
      chmod +x "$script"
      bash "$script"
      ;;
  esac
done
