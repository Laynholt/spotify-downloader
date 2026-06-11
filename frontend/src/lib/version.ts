function parseVersion(value: string): number[] {
    const cleaned = value.trim().replace(/^v/i, "").split(/[+-]/)[0];
    return cleaned.split(".").map((part) => {
        const parsed = Number.parseInt(part, 10);
        return Number.isFinite(parsed) ? parsed : 0;
    });
}

export function isNewerVersion(latest: string, current: string): boolean {
    const latestParts = parseVersion(latest);
    const currentParts = parseVersion(current);
    const maxLength = Math.max(latestParts.length, currentParts.length, 3);
    for (let i = 0; i < maxLength; i++) {
        const latestPart = latestParts[i] || 0;
        const currentPart = currentParts[i] || 0;
        if (latestPart > currentPart)
            return true;
        if (latestPart < currentPart)
            return false;
    }
    return false;
}

