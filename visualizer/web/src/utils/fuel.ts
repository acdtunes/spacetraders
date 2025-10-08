export const getFuelBarColor = (percent: number): string => {
  if (percent >= 75) return '#22c55e';
  if (percent >= 40) return '#facc15';
  if (percent >= 20) return '#f97316';
  return '#ef4444';
};
