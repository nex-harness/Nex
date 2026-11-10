/// <reference types="vite/client" />

declare module "virtual:nex-feature-boundary-graph" {
  import type { FeatureBoundaryGraph } from "@/data/featureBoundaryGraph.js";

  const graph: FeatureBoundaryGraph;
  export default graph;
}
