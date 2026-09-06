// DO NOT EDIT — generated from schema/toolspec.schema.json by scripts/codegen.mts

/**
 * A tool manifest: how a launcher discovers, configures, launches, and monitors one tool.
 */
export interface ToolManifest {
  /**
   * Manifest schema version. v1 is the only version this schema accepts.
   */
  toolspec: 1;
  /**
   * Stable machine-readable tool identifier, unique per discovery source.
   */
  name: string;
  /**
   * Human-readable display name shown in a launcher's tool list.
   */
  title?: string;
  /**
   * One-line description of what the tool does.
   */
  description?: string;
  /**
   * How the launcher runs the tool.
   */
  launch: {
    /**
     * The executable to run.
     */
    command: string;
    /**
     * Argv passed to command. May contain {spec}/{repo}-style placeholder tokens the launcher substitutes.
     */
    args?: string[];
    /**
     * Working directory the command runs in. May contain a {repo}-style placeholder token.
     */
    cwd?: string;
  };
  /**
   * What the tool reads for per-repo configuration.
   */
  config?: {
    /**
     * Path to a JSON Schema the launcher renders a config form from.
     */
    schema?: string;
    /**
     * Files this tool reads, shown to the user but never parsed for values by the launcher.
     */
    files?: string[];
  };
  /**
   * How the launcher observes run status. Omitted entirely means the launcher falls back to exit-code monitoring.
   */
  status?: {
    /**
     * exit-code is the mandatory fallback every tool supports implicitly; ndjson and logfile are opt-in richer status channels.
     */
    kind: "ndjson" | "exit-code" | "logfile";
    /**
     * Which stream the ndjson status events are read from. Only meaningful when kind is ndjson.
     */
    fd?: "stdout" | "stderr";
    /**
     * The event kinds this tool's status stream may emit.
     */
    events?: ("started" | "progress" | "ok" | "failed" | "needs-human")[];
  };
  /**
   * How the launcher checks the tool is installed/runnable before offering it.
   */
  health?: {
    /**
     * Argv of a command that exits 0 when the tool is healthy.
     *
     * @minItems 1
     */
    check: [string, ...string[]];
  };
  /**
   * How the launcher should host this tool's terminal output.
   */
  pane?: {
    /**
     * The launcher's preferred hosting strategy for this tool.
     */
    prefer?: "window" | "split" | "inline";
    /**
     * true = this tool needs a real TTY and must never be run in a captured pane.
     */
    interactive?: boolean;
  };
}
