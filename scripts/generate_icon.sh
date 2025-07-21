#!/bin/bash

# Generate macOS icon from rocket emoji
# Requires ImageMagick and iconutil

set -e

# Create temporary directory
TEMP_DIR=$(mktemp -d)
ICON_SET="$TEMP_DIR/AppIcon.iconset"
mkdir -p "$ICON_SET"

# Create rocket emoji image using ImageMagick
# Use system font to render the rocket emoji
for size in 16 32 64 128 256 512 1024; do
    size2x=$((size * 2))
    
    # Generate standard resolution
    convert -background none -fill black -font "Apple Color Emoji" \
        -pointsize $((size - size/4)) label:"🚀" \
        -gravity center -extent ${size}x${size} \
        "$ICON_SET/icon_${size}x${size}.png"
    
    # Generate @2x resolution (except for 1024)
    if [ $size -lt 1024 ]; then
        convert -background none -fill black -font "Apple Color Emoji" \
            -pointsize $((size2x - size2x/4)) label:"🚀" \
            -gravity center -extent ${size2x}x${size2x} \
            "$ICON_SET/icon_${size}x${size}@2x.png"
    fi
done

# Create the icns file
iconutil -c icns -o "AppIcon.icns" "$ICON_SET"

# Clean up
rm -rf "$TEMP_DIR"

echo "Icon generated: AppIcon.icns"