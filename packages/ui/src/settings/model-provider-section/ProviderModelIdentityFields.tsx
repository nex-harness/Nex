import { useId, type ComponentProps } from "react";
import { MODEL_DISPLAY_NAME_MAX_LENGTH } from "@nex/shared/model-config";
import { Input } from "@/components/ui/input.js";
import { cn } from "@/components/lib/utils.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { TECHNICAL_INPUT_ATTRIBUTES } from "@/lib/technicalInputAttributes.js";
import { modelEditorControlStyle } from "@/settings/model-provider-section/modelEditorControlStyle.js";

type SharedInputHandlers = Pick<
  ComponentProps<typeof Input>,
  "onCompositionStart" | "onCompositionEnd" | "onKeyDown"
>;

/**
 * 模型 ID 与模型名称：ID 是请求使用的真实标识，名称只是对话选择器里的展示别名，
 * 留空时显示名回退为 ID（占位符即展示 ID）。
 */
export function ProviderModelIdentityFields({
  idValue,
  nameValue,
  idReadOnly,
  idAutoFocus,
  onIdChange,
  onIdBlur,
  onNameChange,
  inputHandlers,
}: {
  idValue: string;
  nameValue: string;
  idReadOnly: boolean;
  idAutoFocus: boolean;
  onIdChange: (value: string) => void;
  onIdBlur?: () => void;
  onNameChange: (value: string) => void;
  inputHandlers: SharedInputHandlers;
}) {
  const { intl } = useNexIntl();
  const nameInputId = useId();
  const nameOverridden = nameValue.trim() !== "";
  return (
    <div data-model-identity-row="true" className="flex flex-col gap-4">
      <div className="min-w-0 flex-1">
        <label className="mb-1 block text-ui-base text-foreground-subtle">
          {intl.formatMessage({ id: "settings.modelProvider.modelId" })}
        </label>
        <Input
          {...TECHNICAL_INPUT_ATTRIBUTES}
          type="text"
          autoFocus={idAutoFocus}
          size="lg"
          className={cn("font-mono", modelEditorControlStyle(false))}
          readOnly={idReadOnly}
          value={idValue}
          placeholder={intl.formatMessage({ id: "settings.modelProvider.modelId" })}
          onChange={(event) => onIdChange(event.target.value)}
          onBlur={onIdBlur}
          {...inputHandlers}
        />
      </div>
      <div className="min-w-0 flex-1">
        <label htmlFor={nameInputId} className="mb-1 block text-ui-base text-foreground-subtle">
          {intl.formatMessage({ id: "settings.modelProvider.modelName" })}
        </label>
        <Input
          {...TECHNICAL_INPUT_ATTRIBUTES}
          id={nameInputId}
          type="text"
          size="lg"
          maxLength={MODEL_DISPLAY_NAME_MAX_LENGTH}
          className={modelEditorControlStyle(nameOverridden)}
          data-personal-override={nameOverridden}
          value={nameValue}
          placeholder={idValue.trim() || undefined}
          aria-describedby={`${nameInputId}-help`}
          onChange={(event) => onNameChange(event.target.value)}
          {...inputHandlers}
        />
        <p id={`${nameInputId}-help`} className="mt-1 text-ui-sm text-foreground-subtlest">
          {intl.formatMessage({ id: "settings.modelProvider.modelNameHelp" })}
        </p>
      </div>
    </div>
  );
}
