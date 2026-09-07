import { appendFileSync } from "node:fs";
import { join } from "node:path";

const runtimeRoot = process.env.DIRECTOR_M112_RUNTIME_ROOT;
if (!runtimeRoot) throw new Error("DIRECTOR_M112_RUNTIME_ROOT is required");
appendFileSync(join(runtimeRoot, "install-script-ran"), `${process.argv[2]}\n`);
