const CARGO_ICON_PATTERNS: { regex: RegExp; icon: string }[] = [
  { regex: /(FUEL|HYDROGEN|OIL|GAS|PLASMA|ANTIMATTER)/, icon: '⛽' },
  { regex: /(FOOD|GRAIN|MEAT|PROTEIN|RATIONS|SPICE|BEANS|FRUIT)/, icon: '🍱' },
  { regex: /(WATER|ICE|OXYGEN|HYDRATION)/, icon: '💧' },
  { regex: /(ORE|METAL|IRON|COPPER|ALLOY|STONE|MINERAL)/, icon: '⛏️' },
  { regex: /(MEDIC|PHARMA|DRUG|HEALTH|VACCINE)/, icon: '💊' },
  { regex: /(TECH|ELECTRON|CIRCUIT|MICRO|LASER|COMPUTER|CHIP)/, icon: '💡' },
  { regex: /(MACH|ENGINE|PART|COMPONENT|GEAR|TURBINE)/, icon: '⚙️' },
  { regex: /(ORGANIC|PLANT|FERTIL|BIO|HERB|ALGAE)/, icon: '🌿' },
  { regex: /(TEXTILE|CLOTH|FIBER|FABRIC)/, icon: '🧵' },
  { regex: /(LUX|JEWEL|GEM|CRYSTAL|ART)/, icon: '💎' },
  { regex: /(CHEM|PLASTIC|POLY|SOLVENT|ACID)/, icon: '🧪' },
  { regex: /(WEAPON|AMMO|ARMS|MUNITIONS)/, icon: '💣' },
  { regex: /(DATA|INTEL|SOFTWARE)/, icon: '🧠' },
  { regex: /(CARGO|GENERAL|SUPPLIES)/, icon: '📦' },
];

export const getCargoIcon = (symbol: string): string => {
  const normalized = symbol.toUpperCase();
  for (const entry of CARGO_ICON_PATTERNS) {
    if (entry.regex.test(normalized)) {
      return entry.icon;
    }
  }
  return '📦';
};

export const getCargoLabel = (symbol: string): string => {
  return symbol
    .split('_')
    .map((part) => part.charAt(0) + part.slice(1).toLowerCase())
    .join(' ');
};

export const getCargoShortLabel = (symbol: string): string => {
  const parts = symbol.split('_');
  if (parts.length === 0) return symbol;
  const last = parts[parts.length - 1];
  return last.charAt(0) + last.slice(1).toLowerCase();
};
