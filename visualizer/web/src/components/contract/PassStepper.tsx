import { NOIR, noirAlpha } from '../../theme/noir';
import { CONTRACT_OPS_PASSES, useContractOpsStore } from '../../store/contractOpsStore';

const HINTS = [
  'the contract loop only',
  '+ depot topology: warehouses, hubs, cluster territories',
  '+ the fleet, live',
  '+ event flow',
];

export function PassStepper() {
  const pass = useContractOpsStore((s) => s.pass);
  const setPass = useContractOpsStore((s) => s.setPass);

  return (
    <div className="absolute bottom-4 left-4 rounded p-1 flex items-center gap-1" style={{ background: noirAlpha(NOIR.panel, 0.94) }}>
      <span className="px-2 text-[9px] font-mono tracking-[0.18em]" style={{ color: NOIR.dim }}>
        PASS
      </span>
      {CONTRACT_OPS_PASSES.map((name, i) => (
        <button
          key={name}
          onClick={() => setPass(i)}
          title={HINTS[i]}
          className="px-2.5 py-1 text-[10px] font-mono rounded tracking-wide"
          style={{
            background: pass === i ? NOIR.accent : 'transparent',
            color: pass === i ? NOIR.bg0 : i <= pass ? NOIR.ink : NOIR.muted,
          }}
        >
          {i} {name}
        </button>
      ))}
    </div>
  );
}
