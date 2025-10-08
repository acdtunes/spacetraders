interface WaypointTraitsProps {
  symbol: string;
  traits: { symbol: string }[];
}

export const WaypointTraits = ({ symbol, traits }: WaypointTraitsProps) => {
  if (traits.length === 0) {
    return (
      <span className="col-span-2 text-[8px] text-zinc-500">No notable traits</span>
    );
  }

  return (
    <>
      {traits.map((trait, index) => (
        <span
          key={`${symbol}-trait-${index}`}
          className="bg-sky-500/10 border border-sky-500/30 text-[8px] text-sky-100 rounded px-1 py-0.5"
        >
          {trait.symbol.replace(/_/g, ' ')}
        </span>
      ))}
    </>
  );
};
