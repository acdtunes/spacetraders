import { useState, useMemo } from 'react';
import type { ShipAssignment, OperationType } from '../types/spacetraders';
import { getOperationEmoji, getOperationName, getOperationColor } from '../utils/shipOperations';

interface FleetOperationsSidebarProps {
  assignments: Map<string, ShipAssignment>;
  isVisible: boolean;
  onToggle: () => void;
}

interface GroupedOperations {
  [key: string]: ShipAssignment[];
}

export const FleetOperationsSidebar = ({
  assignments,
  isVisible,
  onToggle,
}: FleetOperationsSidebarProps) => {
  const [expandedSections, setExpandedSections] = useState<Set<string>>(
    new Set(['scout-markets', 'trade', 'mine', 'contract'])
  );

  // Group assignments by operation type
  const groupedOps = useMemo(() => {
    const groups: GroupedOperations = {};

    assignments.forEach((assignment) => {
      if (assignment.status !== 'active' || !assignment.operation) return;

      const opType = assignment.operation;
      if (!groups[opType]) {
        groups[opType] = [];
      }
      groups[opType].push(assignment);
    });

    return groups;
  }, [assignments]);

  const totalActive = useMemo(() => {
    return Array.from(assignments.values()).filter(
      (a) => a.status === 'active'
    ).length;
  }, [assignments]);

  const toggleSection = (opType: string) => {
    setExpandedSections((prev) => {
      const newSet = new Set(prev);
      if (newSet.has(opType)) {
        newSet.delete(opType);
      } else {
        newSet.add(opType);
      }
      return newSet;
    });
  };

  if (!isVisible) {
    // Collapsed tab
    return (
      <div
        className="fixed top-20 right-0 bg-gray-800 border-l border-gray-700 rounded-l-lg cursor-pointer hover:bg-gray-750 transition-colors z-50"
        onClick={onToggle}
        style={{ padding: '12px 8px' }}
      >
        <div className="flex flex-col items-center gap-2">
          <span className="text-gray-400 text-xs writing-mode-vertical transform rotate-180">
            OPERATIONS
          </span>
          <div className="bg-blue-600 text-white text-xs font-bold rounded-full w-6 h-6 flex items-center justify-center">
            {totalActive}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="fixed top-20 right-0 w-80 max-h-[calc(100vh-100px)] bg-gray-800 border-l border-gray-700 rounded-l-lg shadow-xl overflow-hidden z-50 flex flex-col">
      {/* Header */}
      <div className="p-4 border-b border-gray-700 flex items-center justify-between bg-gray-750">
        <div className="flex items-center gap-2">
          <h3 className="text-white font-semibold">Fleet Operations</h3>
          <div className="bg-blue-600 text-white text-xs font-bold rounded-full w-6 h-6 flex items-center justify-center">
            {totalActive}
          </div>
        </div>
        <button
          onClick={onToggle}
          className="text-gray-400 hover:text-white transition-colors"
          aria-label="Close sidebar"
        >
          ✕
        </button>
      </div>

      {/* Operations list */}
      <div className="flex-1 overflow-y-auto p-4 space-y-3">
        {Object.keys(groupedOps).length === 0 ? (
          <div className="text-gray-400 text-sm text-center py-8">
            No active operations
          </div>
        ) : (
          Object.entries(groupedOps).map(([opType, ops]) => {
            const isExpanded = expandedSections.has(opType);
            const emoji = getOperationEmoji(opType as OperationType);
            const name = getOperationName(opType as OperationType);
            const color = getOperationColor(opType as OperationType);

            return (
              <div key={opType} className="bg-gray-700 rounded-lg overflow-hidden">
                {/* Section header */}
                <button
                  onClick={() => toggleSection(opType)}
                  className="w-full p-3 flex items-center justify-between hover:bg-gray-650 transition-colors"
                >
                  <div className="flex items-center gap-2">
                    <span className="text-lg">{emoji}</span>
                    <span className="text-white font-medium">{name}</span>
                    <div
                      className="text-xs font-bold rounded-full w-5 h-5 flex items-center justify-center text-white"
                      style={{ backgroundColor: color }}
                    >
                      {ops.length}
                    </div>
                  </div>
                  <span className="text-gray-400 text-sm">
                    {isExpanded ? '▼' : '▶'}
                  </span>
                </button>

                {/* Expanded list */}
                {isExpanded && (
                  <div className="border-t border-gray-600">
                    {ops.map((assignment) => (
                      <div
                        key={assignment.ship_symbol}
                        className="p-3 border-b border-gray-600 last:border-b-0 hover:bg-gray-650"
                      >
                        <div className="flex items-start justify-between">
                          <div className="flex-1">
                            <div className="text-sm font-mono text-blue-400">
                              {assignment.ship_symbol.split('-').pop()}
                            </div>
                            {assignment.metadata && (
                              <div className="mt-1 text-xs text-gray-400 space-y-0.5">
                                {assignment.metadata.system && (
                                  <div>System: {assignment.metadata.system}</div>
                                )}
                                {assignment.metadata.markets && (
                                  <div>
                                    Markets: {assignment.metadata.markets.length}
                                  </div>
                                )}
                                {assignment.metadata.asteroid && (
                                  <div>
                                    Asteroid:{' '}
                                    {String(assignment.metadata.asteroid).split('-').pop()}
                                  </div>
                                )}
                                {assignment.metadata.market && (
                                  <div>
                                    Market:{' '}
                                    {String(assignment.metadata.market).split('-').pop()}
                                  </div>
                                )}
                              </div>
                            )}
                          </div>
                          <div
                            className="w-2 h-2 rounded-full flex-shrink-0 mt-1"
                            style={{ backgroundColor: color }}
                          />
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>

      {/* Footer */}
      <div className="p-3 border-t border-gray-700 bg-gray-750 text-xs text-gray-400">
        <div className="flex justify-between">
          <span>Total Active</span>
          <span className="text-white font-semibold">{totalActive} ships</span>
        </div>
      </div>
    </div>
  );
};
