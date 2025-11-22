interface SummaryCardProps {
  title: string;
  value: string | number;
  change?: number;
  icon?: string;
  valueColor?: string;
}

export function SummaryCard({ title, value, change, icon, valueColor = 'text-white' }: SummaryCardProps) {
  const formattedChange = change !== undefined ? (change >= 0 ? `+${change.toFixed(1)}%` : `${change.toFixed(1)}%`) : null;
  const changeColor = change && change >= 0 ? 'text-green-400' : 'text-red-400';

  return (
    <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-sm font-medium text-gray-400">{title}</h3>
        {icon && <span className="text-2xl">{icon}</span>}
      </div>
      <div className="flex items-baseline gap-2">
        <p className={`text-2xl font-bold ${valueColor}`}>{value}</p>
        {formattedChange && (
          <span className={`text-sm ${changeColor}`}>{formattedChange}</span>
        )}
      </div>
    </div>
  );
}
