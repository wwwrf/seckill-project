import { SharedArray } from 'k6/data';

export function envStr(name, defaultValue) {
    return (__ENV[name] || defaultValue).toString();
}

export function envNum(name, defaultValue) {
    const raw = __ENV[name];
    if (raw === undefined || raw === null || raw === '') {
        return defaultValue;
    }
    const n = Number(raw);
    return Number.isFinite(n) ? n : defaultValue;
}

export function loadUsers(csvPath) {
    return new SharedArray('users_tokens', function () {
        const csv = open(csvPath);
        const lines = csv.split('\n').filter((line) => line.trim() !== '');
        const records = [];
        for (let i = 1; i < lines.length; i++) {
            const cols = lines[i].split(',');
            if (cols.length >= 2 && cols[1].trim() !== '') {
                records.push({
                    userId: cols[0].trim(),
                    token: cols[1].trim(),
                });
            }
        }
        return records;
    });
}

export function pickUser(users) {
    return users[Math.floor(Math.random() * users.length)];
}

export function authHeaders(token) {
    return {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
    };
}

export function safeJsonParse(raw) {
    try {
        return JSON.parse(raw);
    } catch (e) {
        return null;
    }
}

export function isBizSuccess(respBody) {
    return respBody && respBody.code === 0;
}

export function loadJsonConfig(path) {
    try {
        const raw = open(path);
        return JSON.parse(raw);
    } catch (e) {
        return {};
    }
}

export function pickConfig(profileName, path = './benchmark.config.json') {
    const cfg = loadJsonConfig(path);
    const common = cfg.common || {};
    const profile = (cfg.profiles && cfg.profiles[profileName]) || {};
    return { ...common, ...profile };
}

export function cfgStr(cfg, key, defaultValue) {
    if (cfg && cfg[key] !== undefined && cfg[key] !== null && cfg[key] !== '') {
        return String(cfg[key]);
    }
    return defaultValue;
}

export function cfgNum(cfg, key, defaultValue) {
    if (cfg && cfg[key] !== undefined && cfg[key] !== null && cfg[key] !== '') {
        const n = Number(cfg[key]);
        if (Number.isFinite(n)) {
            return n;
        }
    }
    return defaultValue;
}
