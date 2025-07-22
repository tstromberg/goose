#!/usr/bin/env python3

import os
import subprocess
import tempfile

try:
    from PIL import Image, ImageDraw, ImageFont
    PIL_AVAILABLE = True
except ImportError:
    PIL_AVAILABLE = False

def create_rocket_icon():
    # Create temporary directory for iconset
    temp_dir = tempfile.mkdtemp()
    iconset_path = os.path.join(temp_dir, "AppIcon.iconset")
    os.makedirs(iconset_path)
    
    # Icon sizes needed for macOS
    sizes = [16, 32, 64, 128, 256, 512, 1024]
    
    for size in sizes:
        # Create image with transparent background
        img = Image.new('RGBA', (size, size), (0, 0, 0, 0))
        draw = ImageDraw.Draw(img)
        
        # Draw a white circle background
        padding = int(size * 0.05)
        draw.ellipse([padding, padding, size-padding, size-padding], fill=(255, 255, 255, 255))
        
        # Draw a simple rocket using shapes
        rocket_width = int(size * 0.3)
        rocket_height = int(size * 0.6)
        rocket_x = (size - rocket_width) // 2
        rocket_y = (size - rocket_height) // 2
        
        # Rocket body (red)
        body_points = [
            (rocket_x + rocket_width // 2, rocket_y),  # top (nose)
            (rocket_x + rocket_width, rocket_y + rocket_height * 0.4),  # right side
            (rocket_x + rocket_width, rocket_y + rocket_height * 0.8),  # right bottom
            (rocket_x, rocket_y + rocket_height * 0.8),  # left bottom
            (rocket_x, rocket_y + rocket_height * 0.4),  # left side
        ]
        draw.polygon(body_points, fill=(220, 38, 127, 255))  # Red
        
        # Rocket window (light blue circle)
        window_size = int(rocket_width * 0.4)
        window_x = rocket_x + (rocket_width - window_size) // 2
        window_y = rocket_y + int(rocket_height * 0.25)
        draw.ellipse([window_x, window_y, window_x + window_size, window_y + window_size], 
                     fill=(135, 206, 235, 255))  # Sky blue
        
        # Rocket fins (darker red)
        fin_width = int(rocket_width * 0.3)
        fin_height = int(rocket_height * 0.25)
        fin_y = rocket_y + int(rocket_height * 0.65)
        
        # Left fin
        left_fin = [
            (rocket_x, fin_y),
            (rocket_x - fin_width, fin_y + fin_height),
            (rocket_x, fin_y + fin_height)
        ]
        draw.polygon(left_fin, fill=(178, 34, 34, 255))
        
        # Right fin
        right_fin = [
            (rocket_x + rocket_width, fin_y),
            (rocket_x + rocket_width + fin_width, fin_y + fin_height),
            (rocket_x + rocket_width, fin_y + fin_height)
        ]
        draw.polygon(right_fin, fill=(178, 34, 34, 255))
        
        # Rocket flame (orange/yellow)
        flame_width = int(rocket_width * 0.8)
        flame_height = int(rocket_height * 0.2)
        flame_x = rocket_x + (rocket_width - flame_width) // 2
        flame_y = rocket_y + int(rocket_height * 0.8)
        
        flame_points = [
            (flame_x + flame_width // 2, flame_y + flame_height),  # bottom point
            (flame_x, flame_y),  # left
            (flame_x + flame_width // 4, flame_y - flame_height // 4),  # left-mid
            (flame_x + flame_width // 2, flame_y),  # center
            (flame_x + flame_width * 3 // 4, flame_y - flame_height // 4),  # right-mid
            (flame_x + flame_width, flame_y),  # right
        ]
        draw.polygon(flame_points, fill=(255, 140, 0, 255))  # Orange
        
        # Save standard resolution
        filename = f"icon_{size}x{size}.png"
        img.save(os.path.join(iconset_path, filename))
        
        # Save @2x resolution (except for 1024)
        if size < 1024:
            size_2x = size * 2
            img_2x = Image.new('RGBA', (size_2x, size_2x), (0, 0, 0, 0))
            draw_2x = ImageDraw.Draw(img_2x)
            
            # Draw a white circle background for 2x
            padding_2x = int(size_2x * 0.05)
            draw_2x.ellipse([padding_2x, padding_2x, size_2x-padding_2x, size_2x-padding_2x], fill=(255, 255, 255, 255))
            
            # Scale all rocket dimensions for 2x
            rocket_width_2x = int(size_2x * 0.3)
            rocket_height_2x = int(size_2x * 0.6)
            rocket_x_2x = (size_2x - rocket_width_2x) // 2
            rocket_y_2x = (size_2x - rocket_height_2x) // 2
            
            # Rocket body (red)
            body_points_2x = [
                (rocket_x_2x + rocket_width_2x // 2, rocket_y_2x),
                (rocket_x_2x + rocket_width_2x, rocket_y_2x + rocket_height_2x * 0.4),
                (rocket_x_2x + rocket_width_2x, rocket_y_2x + rocket_height_2x * 0.8),
                (rocket_x_2x, rocket_y_2x + rocket_height_2x * 0.8),
                (rocket_x_2x, rocket_y_2x + rocket_height_2x * 0.4),
            ]
            draw_2x.polygon(body_points_2x, fill=(220, 38, 127, 255))
            
            # Rocket window
            window_size_2x = int(rocket_width_2x * 0.4)
            window_x_2x = rocket_x_2x + (rocket_width_2x - window_size_2x) // 2
            window_y_2x = rocket_y_2x + int(rocket_height_2x * 0.25)
            draw_2x.ellipse([window_x_2x, window_y_2x, window_x_2x + window_size_2x, window_y_2x + window_size_2x], 
                           fill=(135, 206, 235, 255))
            
            # Rocket fins
            fin_width_2x = int(rocket_width_2x * 0.3)
            fin_height_2x = int(rocket_height_2x * 0.25)
            fin_y_2x = rocket_y_2x + int(rocket_height_2x * 0.65)
            
            left_fin_2x = [
                (rocket_x_2x, fin_y_2x),
                (rocket_x_2x - fin_width_2x, fin_y_2x + fin_height_2x),
                (rocket_x_2x, fin_y_2x + fin_height_2x)
            ]
            draw_2x.polygon(left_fin_2x, fill=(178, 34, 34, 255))
            
            right_fin_2x = [
                (rocket_x_2x + rocket_width_2x, fin_y_2x),
                (rocket_x_2x + rocket_width_2x + fin_width_2x, fin_y_2x + fin_height_2x),
                (rocket_x_2x + rocket_width_2x, fin_y_2x + fin_height_2x)
            ]
            draw_2x.polygon(right_fin_2x, fill=(178, 34, 34, 255))
            
            # Rocket flame
            flame_width_2x = int(rocket_width_2x * 0.8)
            flame_height_2x = int(rocket_height_2x * 0.2)
            flame_x_2x = rocket_x_2x + (rocket_width_2x - flame_width_2x) // 2
            flame_y_2x = rocket_y_2x + int(rocket_height_2x * 0.8)
            
            flame_points_2x = [
                (flame_x_2x + flame_width_2x // 2, flame_y_2x + flame_height_2x),
                (flame_x_2x, flame_y_2x),
                (flame_x_2x + flame_width_2x // 4, flame_y_2x - flame_height_2x // 4),
                (flame_x_2x + flame_width_2x // 2, flame_y_2x),
                (flame_x_2x + flame_width_2x * 3 // 4, flame_y_2x - flame_height_2x // 4),
                (flame_x_2x + flame_width_2x, flame_y_2x),
            ]
            draw_2x.polygon(flame_points_2x, fill=(255, 140, 0, 255))
            
            filename_2x = f"icon_{size}x{size}@2x.png"
            img_2x.save(os.path.join(iconset_path, filename_2x))
    
    # Create icns file using iconutil
    try:
        subprocess.run(['iconutil', '-c', 'icns', '-o', 'AppIcon.icns', iconset_path], check=True)
        print("Icon generated: AppIcon.icns")
    except subprocess.CalledProcessError as e:
        print(f"Error creating icns file: {e}")
        return False
    finally:
        # Clean up
        subprocess.run(['rm', '-rf', temp_dir])
    
    return True

if __name__ == "__main__":
    # Check if PIL is available
    if PIL_AVAILABLE:
        try:
            create_rocket_icon()
        except Exception as e:
            print(f"Error with PIL method: {e}")
            PIL_AVAILABLE = False
    
    if not PIL_AVAILABLE:
        print("PIL/Pillow not installed. Creating a basic icon instead...")
        
        # Create a basic icon using ImageMagick as fallback
        temp_dir = tempfile.mkdtemp()
        iconset_path = os.path.join(temp_dir, "AppIcon.iconset")
        os.makedirs(iconset_path)
        
        sizes = [16, 32, 64, 128, 256, 512, 1024]
        
        for size in sizes:
            # Create a simple rocket shape using ImageMagick
            cmd = [
                'magick' if subprocess.run(['which', 'magick'], capture_output=True).returncode == 0 else 'convert',
                '-size', f'{size}x{size}',
                'xc:transparent',
                '-fill', 'red',
                '-draw', f'polygon {size//2},{size//5} {size*3//5},{size*4//5} {size*2//5},{size*4//5}',
                '-fill', 'orange',
                '-draw', f'circle {size//2},{size*4//5} {size//2},{size*9//10}',
                f'{iconset_path}/icon_{size}x{size}.png'
            ]
            subprocess.run(cmd)
            
            if size < 1024:
                size2x = size * 2
                cmd_2x = [
                    'magick' if subprocess.run(['which', 'magick'], capture_output=True).returncode == 0 else 'convert',
                    '-size', f'{size2x}x{size2x}',
                    'xc:transparent',
                    '-fill', 'red',
                    '-draw', f'polygon {size2x//2},{size2x//5} {size2x*3//5},{size2x*4//5} {size2x*2//5},{size2x*4//5}',
                    '-fill', 'orange',
                    '-draw', f'circle {size2x//2},{size2x*4//5} {size2x//2},{size2x*9//10}',
                    f'{iconset_path}/icon_{size}x{size}@2x.png'
                ]
                subprocess.run(cmd_2x)
        
        subprocess.run(['iconutil', '-c', 'icns', '-o', 'AppIcon.icns', iconset_path])
        subprocess.run(['rm', '-rf', temp_dir])
        print("Icon generated: AppIcon.icns")