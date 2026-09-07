import { writeFileSync } from "node:fs";

const marker = process.env.DIRECTOR_LIFECYCLE_MARKER;
if (!marker) throw new Error("DIRECTOR_LIFECYCLE_MARKER is required");
writeFileSync(marker, "admitted lifecycle executed\n", { mode: 0o600 });
