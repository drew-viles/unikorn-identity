/*
Copyright 2024-2025 the Unikorn Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package create

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/unikorn-cloud/core/pkg/constants"
	coreutil "github.com/unikorn-cloud/core/pkg/util"
	unikornv1 "github.com/unikorn-cloud/identity/pkg/apis/unikorn/v1alpha1"
	"github.com/unikorn-cloud/identity/pkg/cmd/errors"
	"github.com/unikorn-cloud/identity/pkg/cmd/factory"
	"github.com/unikorn-cloud/identity/pkg/cmd/flags"
	"github.com/unikorn-cloud/identity/pkg/cmd/util"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type createGroupOptions struct {
	ConfigFlags *genericclioptions.ConfigFlags

	organization flags.HostnameVar
	name         flags.HostnameVar
	description  string
	roles        []string
	users        []string

	organizationID        string
	organizationNamespace string
	roleIDs               []string
	userIDs               []string
}

func (o *createGroupOptions) AddFlags(cmd *cobra.Command, factory *factory.Factory) error {
	cmd.Flags().Var(&o.organization, "organization", "Organization name.")
	cmd.Flags().Var(&o.name, "name", "Group name.")
	cmd.Flags().StringVar(&o.description, "description", "", "A verbose organization description.")
	cmd.Flags().StringSliceVar(&o.roles, "role", nil, "Groups role, may be specified more than once.")
	cmd.Flags().StringSliceVar(&o.users, "user", nil, "Group users, may be specified more than once.")

	if err := cmd.MarkFlagRequired("organization"); err != nil {
		return err
	}

	if err := cmd.MarkFlagRequired("name"); err != nil {
		return err
	}

	if err := cmd.MarkFlagRequired("role"); err != nil {
		return err
	}

	if err := cmd.MarkFlagRequired("user"); err != nil {
		return err
	}

	if err := cmd.RegisterFlagCompletionFunc("organization", factory.ResourceNameCompletionFunc("organization.identity.unikorn-cloud.org", "")); err != nil {
		return err
	}

	if err := cmd.RegisterFlagCompletionFunc("role", factory.ResourceNameCompletionFunc("roles.identity.unikorn-cloud.org", "")); err != nil {
		return err
	}

	if err := cmd.RegisterFlagCompletionFunc("user", factory.UserSubjectCompletionFunc("users.identity.unikorn-cloud.org", "")); err != nil {
		return err
	}

	return nil
}

// validateOrganization ensures the organization doesn't already exist.
func (o *createGroupOptions) validateOrganization(ctx context.Context, cli client.Client) error {
	organization, err := util.GetOrganization(ctx, cli, *o.ConfigFlags.Namespace, o.organization.String())
	if err != nil {
		return err
	}

	o.organizationID = organization.Name
	o.organizationNamespace = organization.Status.Namespace

	return nil
}

// validateGroup ensures the group doesn't already exist.
func (o *createGroupOptions) validateGroup(ctx context.Context, cli client.Client) error {
	requirement, err := labels.NewRequirement(constants.NameLabel, selection.Equals, []string{o.name.String()})
	if err != nil {
		return err
	}

	selector := labels.NewSelector()
	selector = selector.Add(*requirement)

	options := &client.ListOptions{
		Namespace:     *o.ConfigFlags.Namespace,
		LabelSelector: selector,
	}

	var resources unikornv1.GroupList

	if err := cli.List(ctx, &resources, options); err != nil {
		return err
	}

	if len(resources.Items) != 0 {
		return fmt.Errorf("%w: expected no groups to exist with name %s", errors.ErrValidation, o.name.String())
	}

	return nil
}

// validateRole ensures the roles exist and sets the IDs for use later.
func (o *createGroupOptions) validateRoles(ctx context.Context, cli client.Client) error {
	// Remove duplicates.
	slices.Sort(o.roles)
	o.roles = slices.Compact(o.roles)

	options := &client.ListOptions{
		Namespace: *o.ConfigFlags.Namespace,
	}

	var resources unikornv1.RoleList

	if err := cli.List(ctx, &resources, options); err != nil {
		return err
	}

	o.roleIDs = make([]string, len(o.roles))

	for i, role := range o.roles {
		indexer := func(r unikornv1.Role) bool {
			return r.Labels[constants.NameLabel] == role
		}

		index := slices.IndexFunc(resources.Items, indexer)
		if index < 0 {
			return fmt.Errorf("%w: unable to find role %s", errors.ErrValidation, role)
		}

		o.roleIDs[i] = resources.Items[index].Name
	}

	return nil
}

// validateUsers ensures the roles exist and sets the IDs for use later.
func (o *createGroupOptions) validateUsers(ctx context.Context, cli client.Client) error {
	// Remove duplicates.
	slices.Sort(o.users)
	o.users = slices.Compact(o.users)

	options := &client.ListOptions{
		Namespace: o.organizationNamespace,
	}

	var resources unikornv1.UserList

	if err := cli.List(ctx, &resources, options); err != nil {
		return err
	}

	o.userIDs = make([]string, len(o.roles))

	for i, user := range o.users {
		indexer := func(u unikornv1.User) bool {
			return u.Spec.Subject == user
		}

		index := slices.IndexFunc(resources.Items, indexer)
		if index < 0 {
			return fmt.Errorf("%w: unable to find user %s", errors.ErrValidation, user)
		}

		o.userIDs[i] = resources.Items[index].Name
	}

	return nil
}

func (o *createGroupOptions) validate(ctx context.Context, cli client.Client) error {
	validators := []func(context.Context, client.Client) error{
		o.validateOrganization,
		o.validateGroup,
		o.validateRoles,
		o.validateUsers,
	}

	for _, validator := range validators {
		if err := validator(ctx, cli); err != nil {
			return err
		}
	}

	return nil
}

func (o *createGroupOptions) execute(ctx context.Context, cli client.Client) error {
	group := &unikornv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: o.organizationNamespace,
			Name:      coreutil.GenerateResourceID(),
			Labels: map[string]string{
				constants.OrganizationLabel: o.organizationID,
				constants.NameLabel:         o.name.String(),
			},
		},
		Spec: unikornv1.GroupSpec{
			RoleIDs: o.roleIDs,
			UserIDs: o.userIDs,
		},
	}

	annotations := map[string]string{}

	if o.description != "" {
		annotations[constants.DescriptionAnnotation] = o.description
	}

	if len(annotations) > 0 {
		group.Annotations = annotations
	}

	if err := cli.Create(ctx, group); err != nil {
		return err
	}

	return nil
}

func getCreateGroup(factory *factory.Factory) *cobra.Command {
	o := createGroupOptions{
		ConfigFlags: factory.ConfigFlags,
	}

	cmd := &cobra.Command{
		Use:   "group",
		Short: "Create a group",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			client, err := factory.Client()
			if err != nil {
				return err
			}

			if err := o.validate(ctx, client); err != nil {
				return err
			}

			if err := o.execute(ctx, client); err != nil {
				return err
			}

			return nil
		},
	}

	if err := o.AddFlags(cmd, factory); err != nil {
		panic(err)
	}

	return cmd
}
