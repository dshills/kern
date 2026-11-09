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
