import { createHash, randomBytes as rand } from 'node:crypto';
export const text2arr = (str) => {
    const buf = Buffer.from(str, 'utf8');
    return new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
};
// toBase64 is faster, but >=node25
export const arr2base = (data) => Buffer.from(data.buffer, data.byteOffset, data.byteLength).toString('base64');
export const base2arr = (str) => {
    const buf = Buffer.from(str, 'base64');
    return new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
};
export const arr2hex = (data) => Buffer.from(data.buffer, data.byteOffset, data.byteLength).toString('hex');
export const hex2arr = (str) => {
    const buf = Buffer.from(str, 'hex');
    return new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
};
export const hex2bin = (hex) => Buffer.from(hex, 'hex').toString('binary');
export const bin2hex = (bin) => Buffer.from(bin, 'binary').toString('hex');
export async function hash(data, format, algo = 'sha1') {
    algo = algo.replace('sha-', 'sha');
    if (data instanceof ArrayBuffer)
        data = new Uint8Array(data);
    const out = createHash(algo).update(data);
    if (format)
        return out.digest(format);
    const res = out.digest();
    return new Uint8Array(res.buffer, res.byteOffset, res.byteLength);
}
export const randomBytes = (size) => new Uint8Array(rand(size));
export function compare(a, b) {
    const len = a.byteLength < b.byteLength ? a.byteLength : b.byteLength;
    if (len < 128) {
        for (let i = 0; i < len; i++)
            if (a[i] !== b[i])
                return a[i] - b[i];
        return 0;
    }
    return Buffer.compare(a, b);
}
export function equal(a, b) {
    if (a.byteLength !== b.byteLength)
        return false;
    if (a.byteLength < 256) {
        for (let i = 0; i < a.byteLength; i++)
            if (a[i] !== b[i])
                return false;
        return true;
    }
    return Buffer.compare(a, b) === 0;
}
export * from './util.js';
//# sourceMappingURL=node.js.map