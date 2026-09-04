const MEBIBYTE = 1024 * 1024;
export const DEFAULT_MAX_FILE_SIZE_BYTES = 5 * MEBIBYTE;

export function fileSizeLimitLabel(maxFileSizeBytes: number): string {
	return `${Math.round(maxFileSizeBytes / MEBIBYTE)}MB`;
}

export function publicShareURL(
	publicShareBaseURL: string,
	currentOrigin: string,
	slug: string,
	accessCode: string
): string {
	const baseURL = (publicShareBaseURL || currentOrigin).replace(/\/+$/, '');
	return `${baseURL}/res/${encodeURIComponent(slug)}?code=${encodeURIComponent(accessCode)}`;
}
