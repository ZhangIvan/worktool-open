# Parse APK Signing Block (v2/v3) from an APK and extract X.509 certificates (read-only).
# Usage: python -I extract_sig.py <apk_path> <out_dir>
import struct, sys, os

V2_ID = 0x7109871a
V3_ID = 0xf05368c0
V31_ID = 0x1b93ad61

def u32(b, o): return struct.unpack_from('<I', b, o)[0]
def u64(b, o): return struct.unpack_from('<Q', b, o)[0]

def find_signing_block(data):
    # locate EOCD
    eocd = data.rfind(b'PK\x05\x06')
    if eocd < 0:
        raise SystemExit('no EOCD')
    cd_off = u32(data, eocd + 16)
    if cd_off <= 0 or cd_off > len(data):
        # CD offset may be shifted if block inserted; try central dir locate by scanning
        raise SystemExit('bad cd offset %d' % cd_off)
    # signing block sits immediately before central directory
    footer_off = cd_off - 24
    if footer_off < 0:
        raise SystemExit('no signing block footer')
    magic = data[footer_off+8:footer_off+24]
    if magic != b'APK Sig Block 42':
        raise SystemExit('no APK Signing Block (magic not found)')
    blk_len = u64(data, footer_off)  # size of block excluding this size field? footer size = size of block (same as header)
    start = cd_off - blk_len - 8
    hdr_len = u64(data, start)
    if hdr_len != blk_len:
        raise SystemExit('length mismatch')
    return start + 8, footer_off  # entries region [start+8, footer_off)

def iter_pairs(data, lo, hi):
    o = lo
    while o < hi:
        ln = u64(data, o)
        bid = u32(data, o + 8)
        val = data[o + 12: o + 8 + ln]
        yield bid, val
        o += 8 + ln

def lp_seq(buf, off):
    # returns (length, end) of a length-prefixed field at off
    n = struct.unpack_from('<I', buf, off)[0]
    return off + 4, off + 4 + n

def parse_signer_seq(block_val):
    # signer sequence: seq of length-prefixed signers
    out = []
    o = 0
    # outer: length-prefixed sequence
    seq_start, seq_end = lp_seq(block_val, 0)
    p = seq_start
    while p < seq_end:
        s, e = lp_seq(block_val, p)
        out.append((s, e))
        p = e
    return block_val, out

def extract_certs_v2v3(block_val):
    buf, signers = parse_signer_seq(block_val)
    certs = []
    for s, e in signers:
        # signer: signed_data (LP), signatures (LP), publickey (LP)
        sd_s, sd_e = lp_seq(buf, s)
        # signed data: digests (LP), certificates (LP), attributes (LP)
        c1s, c1e = lp_seq(buf, sd_s)          # digests
        cert_seq_s, cert_seq_e = lp_seq(buf, c1e)  # certificates
        p = cert_seq_s
        while p < cert_seq_e:
            cs, ce = lp_seq(buf, p)
            certs.append(buf[cs:ce])
            p = ce
        break  # first signer only
    return certs

def main():
    apk, outdir = sys.argv[1], sys.argv[2]
    with open(apk, 'rb') as f:
        data = f.read()
    lo, hi = find_signing_block(data)
    found = {}
    for bid, val in iter_pairs(data, lo, hi):
        if bid in (V2_ID, V3_ID, V31_ID):
            found[bid] = val
        else:
            print('block id 0x%08x len=%d (other)' % (bid, len(val)))
    for bid, val in found.items():
        scheme = {V2_ID: 'v2', V3_ID: 'v3', V31_ID: 'v3.1'}[bid]
        certs = extract_certs_v2v3(val)
        print('%s block present, signers parsed, certs=%d' % (scheme, len(certs)))
        for i, c in enumerate(certs):
            p = os.path.join(outdir, '%s_cert_%d.der' % (scheme, i))
            with open(p, 'wb') as g:
                g.write(c)
            print('  wrote', p, len(c), 'bytes')
    if not found:
        print('NO v2/v3 blocks found')

if __name__ == '__main__':
    main()
