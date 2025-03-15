#!/bin/bash

# Define the base directory
BASE_DIR="/var/lib/firecracker-containerd/shim-base"

# Define the content
CONTENT="MN2HE43UOVRDA"

# Loop through vm1 to vm9
for i in {9..1}; do
    # Define the directory and file path
    VM_DIR="$BASE_DIR/vm${i}#vm${i}"
    FILE_PATH="$VM_DIR/ctrstub0"

    # Create the directory if it doesn't exist
    sudo mkdir -p "$VM_DIR"

    # Write the content to the file
    echo "$CONTENT $FILE_PATH" | sudo tee "$FILE_PATH" > /dev/null
done
