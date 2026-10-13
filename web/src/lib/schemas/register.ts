import { z } from 'zod';

/** Mirrors auth.MinPasswordLen on the backend. */
export const MIN_PASSWORD = 6;

// Like login.ts, validation messages are i18n message keys resolved via t().
// Mirrors the backend rules in internal/handlers/auth.go (createUser).
export const registerSchema = z
	.object({
		username: z
			.string()
			.trim()
			.min(1, 'register.usernameRequired')
			.regex(/^[A-Za-z0-9._-]{1,64}$/, 'register.usernameInvalid'),
		nickname: z
			.string()
			.trim()
			.min(1, 'register.nicknameRequired')
			.max(64, 'register.nicknameTooLong'),
		password: z.string().min(MIN_PASSWORD, 'register.passwordTooShort'),
		rePassword: z.string().min(1, 'register.rePasswordRequired')
	})
	.refine((data) => data.password === data.rePassword, {
		message: 'register.passwordMismatch',
		path: ['rePassword']
	});

// Settings page "change password" form: same rules, plus the current password.
export const changePasswordSchema = z
	.object({
		currentPassword: z.string().min(1, 'settings.currentPasswordRequired'),
		password: z.string().min(MIN_PASSWORD, 'register.passwordTooShort'),
		rePassword: z.string().min(1, 'register.rePasswordRequired')
	})
	.refine((data) => data.password === data.rePassword, {
		message: 'register.passwordMismatch',
		path: ['rePassword']
	});
