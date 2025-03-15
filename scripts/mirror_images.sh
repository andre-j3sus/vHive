#!/bin/bash

# This script pulls, tags, and pushes pre-converted test images to a local registry.

# Define the list of images and their tags
IMAGES=(
  "curiousgeorgiy/nginx:1.17-alpine-esgz"
  "devandrejesus/hello-world-python:esgz"
  "devandrejesus/image-resizer-python:esgz"
  "devandrejesus/matrix-multiplier-python:esgz"
  "devandrejesus/vector-magnitude-python:esgz"
  "devandrejesus/weather-forecast-go:esgz"
  "devandrejesus/palindrome-checker-go:esgz"
  "devandrejesus/word-counter-go:esgz"
  "devandrejesus/uuid-generator-go:esgz"
)

LOCAL_REGISTRY="localhost:5000"

for IMAGE in "${IMAGES[@]}"; do
  SOURCE="docker.io/$IMAGE"
  DEST="$LOCAL_REGISTRY/$IMAGE"
  
  echo "Processing $IMAGE..."
  
  # Pull the image
  sudo nerdctl pull "$SOURCE" && \
  
  # Tag the image for local registry
  sudo nerdctl tag "$SOURCE" "$DEST" && \
  
  # Push the image to the local registry
  sudo nerdctl push "$DEST"
  
  if [ $? -eq 0 ]; then
    echo "✅ Successfully processed: $IMAGE"
  else
    echo "❌ Failed to process: $IMAGE"
  fi
  
  echo "---------------------------------"
done

echo "🎉 All images processed!"
