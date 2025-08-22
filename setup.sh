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
      exit 0
      ;;
    "")
      echo "Invalid selection. Please try again."
      ;;
    *)
      chmod +x "$script"
      echo "Executing: $script"
      # Handle special scripts that need different execution
      case "$script" in
        "mount-smb-share.sh")
          # Execute directly to preserve $0 for sudo re-execution
          "./mount-smb-share.sh"
          ;;
        *)
          bash "$script"
          ;;
      esac
      echo ""
      echo "Script completed. Select another script or quit."
      echo ""
      # Re-display the menu options
      echo "Available scripts:"
      for i in "${!scripts[@]}"; do
        printf "%2d) %s\n" $((i+1)) "${scripts[$i]}"
      done
      printf "%2d) %s\n" $((${#scripts[@]}+1)) "Quit"
      echo ""
      ;;
  esac
done
