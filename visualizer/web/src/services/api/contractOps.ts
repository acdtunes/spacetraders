import { fetchApi } from './client';
import type { ContractOpsTopology, ContractOpsLive } from '../../types/contractOps';

export async function getContractOpsTopology(): Promise<ContractOpsTopology> {
  return fetchApi<ContractOpsTopology>('/contract-ops/topology');
}

export async function getContractOpsLive(): Promise<ContractOpsLive> {
  return fetchApi<ContractOpsLive>('/contract-ops/live');
}
