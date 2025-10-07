#!/usr/bin/env python3
"""
Analyze geographic partitioning algorithm
"""

markets_coords = [
    ('X1-JB26-J57', -706, 131),
    ('X1-JB26-J56', -590, 110),
    ('X1-JB26-I54', -444, 83),
    ('X1-JB26-I55', -227, 42),
    ('X1-JB26-B7', -169, -298),
    ('X1-JB26-B6', -156, -108),
    ('X1-JB26-K88', -103, 3),
    ('X1-JB26-E43', -53, -8),
    ('X1-JB26-E44', -53, -8),
    ('X1-JB26-E45', -53, -8),
    ('X1-JB26-F47', -29, -67),
    ('X1-JB26-F48', -29, -67),
    ('X1-JB26-A1', -20, -15),
    ('X1-JB26-A2', -20, -15),
    ('X1-JB26-A3', -20, -15),
    ('X1-JB26-A4', -20, -15),
    ('X1-JB26-G49', 10, 63),
    ('X1-JB26-EF5B', 26, 3),
    ('X1-JB26-H50', 32, -33),
    ('X1-JB26-H51', 32, -33),
    ('X1-JB26-H52', 32, -33),
    ('X1-JB26-H53', 32, -33),
    ('X1-JB26-C40', 36, 108),
    ('X1-JB26-C39', 49, 149),
    ('X1-JB26-D41', 65, -54),
    ('X1-JB26-D42', 65, -54),
]

min_x = min(m[1] for m in markets_coords)
max_x = max(m[1] for m in markets_coords)
min_y = min(m[2] for m in markets_coords)
max_y = max(m[2] for m in markets_coords)

width = max_x - min_x
height = max_y - min_y

print('=' * 70)
print('GEOGRAPHIC PARTITIONING ALGORITHM ANALYSIS')
print('=' * 70)
print(f'X range: {min_x} to {max_x} (width: {width})')
print(f'Y range: {min_y} to {max_y} (height: {height})')
print()
print(f'Algorithm decision: X-based partitioning (width {width} > height {height})')
print()

num_ships = 6
slice_width = width / num_ships

print(f'Number of ships: {num_ships}')
print(f'Slice width: {slice_width:.2f} units')
print()
print('Slice boundaries (X-axis):')
for i in range(num_ships):
    slice_start = min_x + (i * slice_width)
    slice_end = min_x + ((i + 1) * slice_width)
    print(f'  Slice {i}: X from {slice_start:7.1f} to {slice_end:7.1f}')

print()
print('=' * 70)
print('MARKET DISTRIBUTION BY SLICE')
print('=' * 70)

ships = ['VOID_HUNTER-2', 'VOID_HUNTER-3', 'VOID_HUNTER-4',
         'VOID_HUNTER-6', 'VOID_HUNTER-7', 'VOID_HUNTER-8']

slices = {i: [] for i in range(num_ships)}

for market, x, y in markets_coords:
    slice_idx = min(int((x - min_x) / slice_width), num_ships - 1)
    slices[slice_idx].append((market, x, y))

for i in range(num_ships):
    print(f'\nSlice {i} → {ships[i]}: {len(slices[i])} markets')
    if slices[i]:
        for market, x, y in slices[i][:8]:
            print(f'  • {market:15s} (X={x:5d}, Y={y:5d})')
        if len(slices[i]) > 8:
            print(f'  ... and {len(slices[i]) - 8} more')
    else:
        slice_start = min_x + (i * slice_width)
        slice_end = min_x + ((i + 1) * slice_width)
        print(f'  ⚠️  EMPTY - No markets in X range {slice_start:.0f} to {slice_end:.0f}')

print()
print('=' * 70)
print('ALGORITHM RULES')
print('=' * 70)
print('1. Calculate bounding box (min/max X and Y)')
print('2. If width > height: Partition by X (vertical slices)')
print('   If height > width: Partition by Y (horizontal slices)')
print('3. Divide range into N equal slices (N = number of ships)')
print('4. Assign each market to the slice it falls into')
print()
print('ISSUE: Geographic partitioning optimizes for SPATIAL separation,')
print('       not WORKLOAD balance. Empty regions create empty slices.')
print()
print('=' * 70)
print('RECOMMENDATIONS')
print('=' * 70)
print('Option 1: Use K-means clustering for balanced market distribution')
print('Option 2: Redistribute markets from large slices to empty slices')
print('Option 3: Use hybrid: geographic + rebalancing for slices <2 markets')
print()
