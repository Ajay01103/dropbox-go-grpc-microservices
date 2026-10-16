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
import { useRegister } from "@/modules/auth/api/use-register";
import {
  messageForAuthError,
  validateEmail,
  validateName,
  validatePassword,
} from "@/modules/auth/lib/auth-errors";

/**
 * Sign-up. The auth service returns a token pair from Register, so a successful
 * registration is also a sign-in: the session store flips and the guards move
 * the user to the tab stack, exactly like the login screen.
 */
export default function RegisterScreen() {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [agreed, setAgreed] = useState(true);
  const [fieldErrors, setFieldErrors] = useState<{
    name?: string | null;
    email?: string | null;
    password?: string | null;
  }>({});

  const register = useRegister();

  const submit = () => {
    const nextErrors = {
      name: validateName(name),
      email: validateEmail(email),
      password: validatePassword(password),
    };
    setFieldErrors(nextErrors);
    if (nextErrors.name || nextErrors.email || nextErrors.password) return;

    register.mutate({ name, email, password });
  };

  const clearError = (field: "name" | "email" | "password") => {
    setFieldErrors((current) => (current[field] ? { ...current, [field]: null } : current));
  };

  return (
    <AuthScreen onBack={() => router.back()} showBack>
      <AuthHeading
        subtitle="Create an account to unlock all features and get the most out of Dropbox."
        title="Create an account"
      />

      <AuthForm>
        <AuthField
          autoComplete="name"
          error={fieldErrors.name}
          icon="person-outline"
          label="Name"
          placeholder="Example name"
          returnKeyType="next"
          value={name}
          onChangeText={(next) => {
            setName(next);
            clearError("name");
          }}
        />

        <AuthField
          autoComplete="email"
          error={fieldErrors.email}
          icon="mail-outline"
          keyboardType="email-address"
          label="E-mail"
          placeholder="example@gmail.com"
          returnKeyType="next"
          value={email}
          onChangeText={(next) => {
            setEmail(next);
            clearError("email");
          }}
        />

        <AuthField
          autoComplete="new-password"
          error={fieldErrors.password}
          icon="lock-closed-outline"
          label="Password"
          placeholder="••••••••"
          returnKeyType="go"
          secureTextEntry
          value={password}
          onChangeText={(next) => {
            setPassword(next);
            clearError("password");
          }}
          onSubmitEditing={submit}
        />

        <AuthRememberRow checked={agreed} label="Remember me" onChange={setAgreed} />

        <AuthErrorBanner message={register.isError ? messageForAuthError(register.error) : null} />

        <AuthPrimaryButton label="Sign up" loading={register.isPending} onPress={submit} />

        <AuthDivider label="Or Log in with" />

        <AuthSocialRow
          onUnavailable={(provider) =>
            Alert.alert(
              "Not available yet",
              `${provider} sign-up is not connected to the backend yet.`,
            )
          }
        />

        <AuthFooterPrompt
          actionLabel="Log in"
          question="Already have an account?"
          onPress={() => router.replace("/login")}
        />
      </AuthForm>
    </AuthScreen>
  );
}
