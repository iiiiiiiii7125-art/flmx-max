export interface NyImportItem {
  dest: string[];
  listen_port: number | null;
  name: string;
}

export interface ParsedNyImportLine {
  line: string;
  parsed?: NyImportItem;
  error?: string;
}

// 目标地址校验：host:port。host 可为域名/IPv4，或方括号包裹的 IPv6
// （与主表单 ipv6FullPattern、后端 SplitHostPort 口径一致）；端口须为 1-65535。
export const isValidForwardDestAddress = (value: string): boolean => {
  const trimmed = value.trim();

  if (!trimmed) {
    return false;
  }

  let portPart: string;

  if (trimmed.startsWith("[")) {
    const closeIdx = trimmed.indexOf("]");

    if (closeIdx <= 1) {
      return false;
    }

    const host = trimmed.slice(1, closeIdx);
    const rest = trimmed.slice(closeIdx + 1);

    if (
      !rest.startsWith(":") ||
      host.includes("[") ||
      host.includes("]")
    ) {
      return false;
    }

    portPart = rest.slice(1);
  } else {
    const colonIdx = trimmed.lastIndexOf(":");

    if (colonIdx <= 0) {
      return false;
    }

    const host = trimmed.slice(0, colonIdx);

    if (host.includes(":") || host.includes("[") || host.includes("]")) {
      return false;
    }

    portPart = trimmed.slice(colonIdx + 1);
  }

  if (!/^\d+$/.test(portPart)) {
    return false;
  }

  const port = Number(portPart);

  return port >= 1 && port <= 65535;
};

const getAliasField = (
  item: Record<string, unknown>,
  aliases: string[],
): unknown => {
  for (const alias of aliases) {
    if (Object.prototype.hasOwnProperty.call(item, alias)) {
      return item[alias];
    }
  }

  return undefined;
};

const normalizeDestList = (value: unknown): string[] | null => {
  if (Array.isArray(value)) {
    const normalized = value.map((itemValue) =>
      typeof itemValue === "string" ? itemValue.trim() : "",
    );

    if (normalized.some((itemValue) => itemValue === "")) {
      return null;
    }

    return normalized;
  }

  if (typeof value === "string") {
    const normalized = value
      .split(",")
      .map((itemValue) => itemValue.trim())
      .filter((itemValue) => itemValue !== "");

    return normalized.length > 0 ? normalized : null;
  }

  return null;
};

const normalizeListenPort = (value: unknown): number | null | undefined => {
  if (value === undefined || value === null || value === "") {
    return null;
  }

  if (typeof value === "number") {
    return Number.isInteger(value) ? value : undefined;
  }

  if (typeof value === "string") {
    const trimmed = value.trim();

    if (!trimmed) {
      return null;
    }

    if (!/^\d+$/.test(trimmed)) {
      return undefined;
    }

    return Number.parseInt(trimmed, 10);
  }

  return undefined;
};

const isValidListenPort = (value: unknown): value is number => {
  return (
    typeof value === "number" &&
    Number.isFinite(value) &&
    value >= 1 &&
    value <= 65535
  );
};

const validateNyItem = (line: string, value: unknown): ParsedNyImportLine => {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { line, error: "JSON结构错误" };
  }

  const item = value as Record<string, unknown>;
  const dest = getAliasField(item, ["dest", "dst", "target", "targets"]);
  const listenPortRaw = getAliasField(item, [
    "listen_port",
    "listenPort",
    "port",
    "in_port",
    "inPort",
  ]);
  const name = getAliasField(item, ["name", "forward_name", "forwardName"]);
  const normalizedDest = normalizeDestList(dest);
  const normalizedListenPort = normalizeListenPort(listenPortRaw);

  if (!normalizedDest || normalizedDest.length === 0) {
    return { line, error: "dest数组为空或格式错误" };
  }

  if (typeof name !== "string" || name.trim() === "") {
    return { line, error: "name不能为空" };
  }

  if (normalizedListenPort === undefined) {
    return { line, error: "listen_port格式错误，应为1-65535之间的数字" };
  }

  if (
    normalizedListenPort !== null &&
    !isValidListenPort(normalizedListenPort)
  ) {
    return { line, error: "listen_port必须为1-65535之间的数字" };
  }

  const invalid = normalizedDest.find(
    (itemValue) => !isValidForwardDestAddress(itemValue),
  );

  if (invalid) {
    return { line, error: `目标地址格式错误: ${invalid}` };
  }

  return {
    line,
    parsed: {
      dest: normalizedDest,
      listen_port: normalizedListenPort,
      name: name.trim(),
    },
  };
};

const splitConcatenatedJsonObjects = (input: string): string[] => {
  const result: string[] = [];
  let depth = 0;
  let start = -1;
  let inString = false;
  let escaping = false;

  for (let i = 0; i < input.length; i += 1) {
    const char = input[i];

    if (escaping) {
      escaping = false;
      continue;
    }

    if (char === "\\") {
      escaping = true;
      continue;
    }

    if (char === '"') {
      inString = !inString;
      continue;
    }

    if (inString) {
      continue;
    }

    if (char === "{") {
      if (depth === 0) {
        start = i;
      }
      depth += 1;
      continue;
    }

    if (char === "}") {
      depth -= 1;
      if (depth === 0 && start >= 0) {
        result.push(input.slice(start, i + 1));
        start = -1;
      }
    }
  }

  return result;
};

export const parseNyFormatData = (input: string): ParsedNyImportLine[] => {
  const trimmed = input.trim();

  if (!trimmed) {
    return [];
  }

  const parsedResults: ParsedNyImportLine[] = [];
  const objectChunks = splitConcatenatedJsonObjects(trimmed);

  if (objectChunks.length > 0) {
    objectChunks.forEach((chunk) => {
      try {
        const parsed = JSON.parse(chunk);

        parsedResults.push(validateNyItem(chunk, parsed));
      } catch {
        parsedResults.push({ line: chunk, error: "JSON解析失败" });
      }
    });

    return parsedResults;
  }

  trimmed
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .forEach((line) => {
      try {
        const parsed = JSON.parse(line);

        parsedResults.push(validateNyItem(line, parsed));
      } catch {
        parsedResults.push({ line, error: "JSON解析失败" });
      }
    });

  return parsedResults;
};

export const convertNyItemToForwardInput = (item: NyImportItem) => {
  return {
    name: item.name.trim(),
    inPort: item.listen_port,
    remoteAddr: item.dest.join(","),
    strategy: "fifo" as const,
  };
};
