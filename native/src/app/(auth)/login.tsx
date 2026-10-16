import { router } from "expo-router";
import { useState } from "react";
import { Alert } from "react-native";

import { AuthField } from "@/modules/auth/components/auth-field";
import {
  AuthDivider,
  AuthFooterPrompt,
  AuthPrimaryButton,
  AuthRememberRow,
  AuthSocialRow,
} from "@/modules/auth/components/auth-controls";
import {
  AuthErrorBanner,
  AuthForm,
  AuthHeading,
  AuthScreen,
} from "@/modules/auth/components/auth-screen";
import { useLogin } from "@/modules/auth/api/use-login";
import {
  messageForAuthError,
  validateEmail,
  validatePassword,
} from "@/modules/auth/lib/auth-errors";

/**
 * Sign-in.
 *
 * Nothing here navigates on success: logging in flips the session store, and
 * the root layout's `Stack.Protected` guards swap the auth stack for the tab
 * stack. Keeping that decision in one place is why there is no `router.replace`
 * after the mutation.
 */
export default function LoginScreen() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [rememberMe, setRememberMe] = useState(true);
  const [emailError, setEmailError] = useState<string | null>(null);
  const [passwordError, setPasswordError] = useState<string | null>(null);

  const login = useLogin();

  const submit = () => {
    const nextEmailError = validateEmail(email);
    const nextPasswordError = validatePassword(password);
    setEmailError(nextEmailError);
    setPasswordError(nextPasswordError);
    if (nextEmailError || nextPasswordError) return;

    login.mutate({ email, password });
  };

  return (
    <AuthScreen onBack={() => router.back()} showBack={router.canGoBack()}>
      <AuthHeading
        subtitle="Welcome to Dropbox. Log in now to access your account and all the features built for you."
        title="Welcome to Login"
      />

      <AuthForm>
        <AuthField
          autoComplete="email"
          error={emailError}
          icon="mail-outline"
          keyboardType="email-address"
          label="E-mail"
          placeholder="example@gmail.com"
          returnKeyType="next"
          value={email}
          onChangeText={(next) => {
            setEmail(next);
            if (emailError) setEmailError(null);
          }}
        />

        <AuthField
          autoComplete="current-password"
          error={passwordError}
          icon="lock-closed-outline"
          label="Password"
          placeholder="••••••••"
          returnKeyType="go"
          secureTextEntry
          value={password}
          onChangeText={(next) => {
            setPassword(next);
            if (passwordError) setPasswordError(null);
          }}
          onSubmitEditing={submit}
        />

        <AuthRememberRow
          checked={rememberMe}
          label="Remember me"
          onChange={setRememberMe}
          trailing={{
            label: "Forgot password?",
            onPress: () => announceUnavailable("Password reset"),
          }}
        />

        <AuthErrorBanner message={login.isError ? messageForAuthError(login.error) : null} />

        <AuthPrimaryButton label="Log in" loading={login.isPending} onPress={submit} />

        <AuthDivider label="Or Log in with" />

        <AuthSocialRow onUnavailable={(provider) => announceUnavailable(`${provider} sign-in`)} />

        <AuthFooterPrompt
          actionLabel="Create an account"
          question="Didn't have an account?"
          onPress={() => router.push("/register")}
        />
      </AuthForm>
    </AuthScreen>
  );
}

/**
 * Password reset and social sign-in are designed but not wired to anything yet.
 * Saying so is better than a link that navigates nowhere.
 */
function announceUnavailable(feature: string): void {
  Alert.alert("Not available yet", `${feature} is not connected to the backend yet.`);
}
