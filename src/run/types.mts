/**
 * The slice of a tool manifest this launcher reads.
 *
 * The contract package (`@hive-controls/toolspec`) owns the schema and the generated
 * types for all of it; this is deliberately a separate, smaller declaration of only
 * the fields `formic run` touches. Declaring it here is what lets the launcher build
 * and run with the contract package absent — it validates against the real schema when
 * that package IS installed (manifest.mts), and is honest about it when it is not.
 */
export interface ManifestVariable {
  name: string;
  description?: string;
  secret?: boolean;
  default?: string;
  required?: boolean;
}

export interface ToolManifest {
  toolspec: number;
  name: string;
  title?: string;
  description?: string;
  launch: {
    command: string;
    args?: string[];
    cwd?: string;
  };
  env?: ManifestVariable[];
  health?: {
    check?: string[];
  };
}
