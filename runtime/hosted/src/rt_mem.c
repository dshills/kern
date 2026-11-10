// kern hosted runtime: pages from the operating system (std/mem,
// specs/runtime RF3).
// Included by kern_rt.c; see kern_rt.h for the entries.

int64_t kern_mem_page_size(void) { return (int64_t)kern_page; }
void *kern_mem_map(int64_t bytes) {
    if (bytes <= 0) return NULL;
    void *p = mmap(NULL, (size_t)bytes, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANON, -1, 0);
    return p == MAP_FAILED ? NULL : p;
}
void kern_mem_unmap(void *p, int64_t bytes) {
    if (p != NULL && bytes > 0) (void)munmap(p, (size_t)bytes);
}
// kern_mem_map_file maps the first len bytes of an open file, shared, so
// that a write through the mapping is a write to the file: the address,
// or -errno (specs/runtime RL4). An address is never in the range of the
// negated error numbers.
int64_t kern_mem_map_file(int32_t fd, int64_t len, uint8_t writable) {
    if (len <= 0) return -EINVAL;
    void *p = mmap(NULL, (size_t)len, writable ? PROT_READ | PROT_WRITE : PROT_READ, MAP_SHARED, fd, 0);
    return p == MAP_FAILED ? -(int64_t)errno : (int64_t)(uintptr_t)p;
}
// kern_mem_sync returns when the system has written a mapping's changed
// pages to its file: 0 or -errno.
int32_t kern_mem_sync(void *p, int64_t len) {
    if (p == NULL || len <= 0) return 0;
    return msync(p, (size_t)len, MS_SYNC) < 0 ? -errno : 0;
}
