#!/bin/bash

# Generate macOS icon from rocket emoji
# Requires either Python 3 or ImageMagick and iconutil

set -e

# Get the directory where this script is located
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Try Python method first (more reliable for emoji)
if command -v python3 &> /dev/null && [ -f "$SCRIPT_DIR/generate_icon.py" ]; then
    echo "Using Python to generate icon..."
    python3 "$SCRIPT_DIR/generate_icon.py"
    exit 0
fi

# Fallback to ImageMagick method
echo "Python not available, using ImageMagick..."

# Create temporary directory
TEMP_DIR=$(mktemp -d)
ICON_SET="$TEMP_DIR/AppIcon.iconset"
mkdir -p "$ICON_SET"

# Check if we should use 'magick' or 'convert'
if command -v magick &> /dev/null; then
    CONVERT_CMD="magick"
else
    CONVERT_CMD="convert"
fi

# Create a simple rocket shape icon
for size in 16 32 64 128 256 512 1024; do
    size2x=$((size * 2))
    
    # Generate standard resolution - simple rocket shape
    $CONVERT_CMD -size ${size}x${size} xc:transparent \
        -fill red -draw "polygon $((size/2)),$((size/5)) $((size*3/5)),$((size*4/5)) $((size*2/5)),$((size*4/5))" \
        -fill orange -draw "circle $((size/2)),$((size*4/5)) $((size/2)),$((size*9/10))" \
        "$ICON_SET/icon_${size}x${size}.png"
    
    # Generate @2x resolution (except for 1024)
    if [ $size -lt 1024 ]; then
        $CONVERT_CMD -size ${size2x}x${size2x} xc:transparent \
            -fill red -draw "polygon $((size2x/2)),$((size2x/5)) $((size2x*3/5)),$((size2x*4/5)) $((size2x*2/5)),$((size2x*4/5))" \
            -fill orange -draw "circle $((size2x/2)),$((size2x*4/5)) $((size2x/2)),$((size2x*9/10))" \
            "$ICON_SET/icon_${size}x${size}@2x.png"
    fi
done

# Create the icns file
iconutil -c icns -o "AppIcon.icns" "$ICON_SET"

# Clean up
rm -rf "$TEMP_DIR"

echo "Icon generated: AppIcon.icns"